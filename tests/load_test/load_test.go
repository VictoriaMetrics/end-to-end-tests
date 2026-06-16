package load_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/gather"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestLoadTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Load test Suite", suiteConfig, reporterConfig)
}

var (
	t terratesting.TestingT
)

func selectK6Scenario(k6Scenario string, enableHPA bool) string {
	if enableHPA {
		return "ramping-metrics"
	}
	if k6Scenario != "" {
		return k6Scenario
	}
	return "prw2-50vus-10mins"
}

// Install shared infra once on process 1; all processes receive their own t.
var _ = SynchronizedAfterSuite(
	func(ctx context.Context) {},
	func(ctx context.Context) {
		t := tests.GetT()
		overwatchKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		gather.RestartOverwatchInstance(ctx, t, overwatchKubeOpts)
	},
)

var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t := tests.GetT()

		// Stage 1 (parallel): discover ingress host + install k6 + install chaos mesh.
		// K6 and ChaosMesh have no dependency on the nginx host.
		var wg sync.WaitGroup
		chaosCfg := tests.DefaultChaosMeshConfig()
		wg.Add(3)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.DiscoverIngressHost(ctx, t)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallK6(ctx, t, consts.K6OperatorNamespace)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallChaosMesh(ctx, chaosCfg.HelmChart, chaosCfg.ValuesFile, t, chaosCfg.Namespace, chaosCfg.ReleaseName)
		}()
		wg.Wait()

		// Stage 2 (parallel): install vmgather + vm k8s stack (both need nginx host from stage 1).
		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMGather(ctx, t)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMK8StackWithHelm(
				ctx,
				consts.VMK8sStackChart,
				consts.SmokeValuesFile(),
				t,
				consts.DefaultVMNamespace,
				consts.DefaultReleaseName,
			)
			install.InstallVictoriaLogs(ctx, t, consts.DefaultVMNamespace, consts.DefaultVLReleaseName, consts.DefaultVLCollectorReleaseName)
		}()
		wg.Wait()

		// Stage 3 (parallel): overwatch + delete stock vmcluster + alert rules (all need vmk8stack).
		defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		wg.Add(3)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			// Remove the stock helm-managed VMCluster; each load test creates its own.
			install.DeleteVMCluster(t, defaultKubeOpts, consts.DefaultReleaseName)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.AddCustomAlertRules(ctx, t, consts.DefaultVMNamespace)
		}()
		wg.Wait()
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = Describe("Load tests", Label("load-test"), func() {

	// LoadScenario holds configuration for a single load test run.
	type LoadScenario struct {
		ScenarioName string
		// K6Scenario is the base name of the k6 JavaScript file under manifests/load-tests/
		// (without the .js extension). Defaults to "prw2-50vus-10mins" when empty.
		K6Scenario string
		Patches    []jsonpatch.Patch
		EnableLB   bool
		// SetupFunc, if non-nil, is called after the namespace is created but before VMCluster
		// installation. It can be used to provision supporting infrastructure (e.g. NFS server)
		// and returns additional patches to apply to the VMCluster manifest.
		PreInstallFunc func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) []jsonpatch.Patch
		// SetupFunc, if non-nil, is called after VMCluster installation and autoscaler setup but
		// before the k6 run. It can be used to start background chaos scenarios or other
		// post-install operations. The scenario runs autonomously; SetupFunc does not block on it.
		SetupFunc func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string)
		// EnableHPA, if true, installs a Kubernetes HorizontalPodAutoscaler targeting the
		// requestsLoadBalancer VMAuth Deployment (vmauth-<clusterName>). Requires EnableLB.
		EnableHPA bool
		// ExtraEnvVarsFunc, if non-nil, is called with the test namespace and returns
		// additional environment variables to pass to the k6 runner. Use this to override
		// default URLs (e.g. VMINSERT_URL) when traffic should flow through a proxy such
		// as VMAgent instead of hitting VMInsert directly.
		ExtraEnvVarsFunc func(namespace string) map[string]string
		// K6MaxDuration overrides the default consts.K6JobMaxDuration wait timeout when
		// waiting for k6 runner jobs to complete. Use this for multi-phase scenarios that
		// run longer than the default 20 minutes. Zero means use the default.
		K6MaxDuration    time.Duration
		VerificationFunc func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string)
	}

	// vmAgentSetupFunc deploys a VMAgent in the test namespace configured to forward
	// all received data to the VMInsert service of the local VMCluster. This lets
	// k6 push metrics through VMAgent instead of hitting VMInsert directly, validating
	// the full VMAgent→VMInsert→VMStorage ingestion path.
	vmAgentSetupFunc := func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) {
		vmClient := install.GetVMClient(t, kubeOpts)
		vminsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write",
			consts.GetVMInsertSvc(namespace, namespace))
		rwPatch := tests.NewJSONPatchBuilder().
			Add("/spec/remoteWrite", []map[string]string{{"url": vminsertURL}}).
			MustBuild()
		install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmClient, []jsonpatch.Patch{rwPatch})
	}

	vmStorageCyclingSetupFunc := func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) {
		// Apply the vmstorage pod restart workflow. Chaos Mesh runs it autonomously:
		// kills pod-0, waits 90s, then kills pod-1 — exactly once within a 10m deadline.
		install.ApplyChaosScenario(ctx, t, namespace, "pods", "vmstorage-pod-restart-cycling")
	}

	// vmStorageSlownessSetupFunc simulates a slow vmstorage-0 by injecting 500ms network
	// delay on all vminsert→vmstorage-0 connections for 8 minutes. This forces the
	// improved slowness-based rerouting logic (PR #9945) to trigger: only the slowest
	// storage node should receive rerouted rows, with no rerouting storm.
	vmStorageSlownessSetupFunc := func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) {
		install.ApplyChaosScenario(ctx, t, namespace, "network", "vminsert-to-vmstorage0-slowness")
	}

	runLoadScenario := func(ctx context.Context, scenario LoadScenario) {
		overwatch, err := tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		scenarioName := scenario.ScenarioName
		namespace := fmt.Sprintf("vm-load-%s", scenarioName)

		kubeOpts := k8s.NewKubectlOptions("", "", namespace)

		DeferCleanup(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailure(ctx, t, kubeOpts, namespace)

			install.DeleteVMCluster(t, kubeOpts, namespace)
			if scenario.PreInstallFunc != nil {
				install.DeleteNFSResources(ctx, t, namespace)
			}
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		tests.CleanupNamespace(t, kubeOpts, namespace)
		tests.EnsureNamespaceExists(t, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "vm-load-test=true", "--overwrite")

		vmClient := install.GetVMClient(t, kubeOpts)
		clusterName := namespace

		affinity := map[string]interface{}{
			"podAffinity": map[string]interface{}{
				"preferredDuringSchedulingIgnoredDuringExecution": []map[string]interface{}{
					{
						"weight": 100,
						"podAffinityTerm": map[string]interface{}{
							"topologyKey": "kubernetes.io/hostname",
							"labelSelector": map[string]interface{}{
								"matchExpressions": []map[string]interface{}{
									{
										"key":      "app.kubernetes.io/instance",
										"operator": "In",
										"values":   []string{clusterName},
									},
								},
							},
						},
					},
				},
			},
			"podAntiAffinity": map[string]interface{}{
				"requiredDuringSchedulingIgnoredDuringExecution": []map[string]interface{}{
					{
						"topologyKey": "kubernetes.io/hostname",
						"namespaceSelector": map[string]interface{}{
							"matchLabels": map[string]interface{}{
								"vm-load-test": "true",
							},
						},
						"labelSelector": map[string]interface{}{
							"matchExpressions": []map[string]interface{}{
								{
									"key":      "app.kubernetes.io/instance",
									"operator": "Exists",
								},
								{
									"key":      "app.kubernetes.io/instance",
									"operator": "NotIn",
									"values":   []string{clusterName},
								},
							},
						},
					},
				},
			},
		}

		patches := scenario.Patches
		if scenario.PreInstallFunc != nil {
			extraPatches := scenario.PreInstallFunc(ctx, kubeOpts, namespace)
			patches = append(patches, extraPatches...)
		}
		for _, component := range []string{"vminsert", "vmselect", "vmstorage"} {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/metadata/name", clusterName).
				Add(fmt.Sprintf("/spec/%s/affinity", component), affinity).
				MustBuild())
		}
		const lbCPULimit = "250m"
		const lbMemLimit = "500Mi"
		if scenario.EnableLB {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/requestsLoadBalancer", map[string]string{}).
				Add("/spec/requestsLoadBalancer/enabled", true).
				Add("/spec/requestsLoadBalancer/spec", map[string]string{}).
				Add("/spec/requestsLoadBalancer/spec/replicaCount", 1).
				Add("/spec/requestsLoadBalancer/spec/resources", map[string]string{}).
				Add("/spec/requestsLoadBalancer/spec/resources/limits", map[string]string{}).
				Add("/spec/requestsLoadBalancer/spec/resources/limits/cpu", lbCPULimit).
				Add("/spec/requestsLoadBalancer/spec/resources/limits/memory", lbMemLimit).
				Add("/spec/requestsLoadBalancer/spec/affinity", affinity).
				Add("/spec/requestsLoadBalancer/spec/nodeSelector", map[string]string{"monitoring": "true"}).
				Add("/spec/requestsLoadBalancer/spec/tolerations", []map[string]interface{}{
					{"key": "monitoring", "operator": "Exists", "effect": "NoSchedule"},
				}).
				MustBuild())
		}

		// Nodes are dedicated (4 CPU / 13.3Gi allocatable). DaemonSets consume ~258m CPU,
		// monitoring pods run on non-monitoring nodes, LB keeps 250m CPU / 500Mi mem,
		// leaving ~3492m CPU and ~12.8Gi for 6 cluster pods.
		type componentResources struct{ cpuReq, memReq, memLimit string }
		componentResourceMap := map[string]componentResources{
			"vminsert":  {"400m", "500Mi", "1Gi"},
			"vmselect":  {"400m", "1Gi", "2Gi"},
			"vmstorage": {"600m", "2Gi", "3Gi"},
		}
		for component, res := range componentResourceMap {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add(fmt.Sprintf("/spec/%s/resources/requests/cpu", component), res.cpuReq).
				Add(fmt.Sprintf("/spec/%s/resources/requests/memory", component), res.memReq).
				Add(fmt.Sprintf("/spec/%s/resources/limits/memory", component), res.memLimit).
				MustBuild())
		}
		if scenario.EnableLB {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/requestsLoadBalancer", map[string]string{}).
				Add("/spec/requestsLoadBalancer/enabled", true).
				Add("/spec/requestsLoadBalancer/spec", map[string]string{}).
				Add("/spec/requestsLoadBalancer/spec/replicaCount", 1).
				Add("/spec/requestsLoadBalancer/spec/resources", map[string]string{}).
				Add("/spec/requestsLoadBalancer/spec/resources/limits", map[string]string{}).
				Add("/spec/requestsLoadBalancer/spec/resources/limits/cpu", lbCPULimit).
				Add("/spec/requestsLoadBalancer/spec/resources/limits/memory", lbMemLimit).
				Add("/spec/requestsLoadBalancer/spec/affinity", affinity).
				Add("/spec/requestsLoadBalancer/spec/nodeSelector", map[string]string{"monitoring": "true"}).
				Add("/spec/requestsLoadBalancer/spec/tolerations", []map[string]interface{}{
					{"key": "monitoring", "operator": "Exists", "effect": "NoSchedule"},
				}).
				MustBuild())
		}

		if scenario.EnableHPA {
			// Configure HPAs via VMCluster spec so the operator manages them natively
			// and preserves existing replicas on reconciliation.
			hpaMetrics := []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name":   "cpu",
						"target": map[string]interface{}{"type": "Utilization", "averageUtilization": int32(50)},
					},
				},
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name":   "memory",
						"target": map[string]interface{}{"type": "Utilization", "averageUtilization": int32(80)},
					},
				},
			}
			scaleUpBehaviour := map[string]interface{}{
				"stabilizationWindowSeconds": int32(30),
				"policies": []interface{}{
					map[string]interface{}{"type": "Pods", "value": int32(1), "periodSeconds": int32(30)},
				},
			}
			scaleDownPolicy := []interface{}{
				map[string]interface{}{"type": "Pods", "value": int32(1), "periodSeconds": int32(60)},
			}
			for _, component := range []struct {
				name string
			}{
				{"vminsert"},
				{"vmselect"},
			} {
				patches = append(patches, tests.NewJSONPatchBuilder().
					Add(fmt.Sprintf("/spec/%s/hpa", component.name), map[string]interface{}{
						"minReplicas": int32(1),
						"maxReplicas": int32(4),
						"metrics":     hpaMetrics,
						"behaviour": map[string]interface{}{
							"scaleUp": scaleUpBehaviour,
							"scaleDown": map[string]interface{}{
								"stabilizationWindowSeconds": int32(120),
								"policies":                   scaleDownPolicy,
							},
						},
					}).
					MustBuild())
			}
			// vmstorage: operator webhook rejects scaleDown behaviour — omit it.
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/vmstorage/hpa", map[string]interface{}{
					"minReplicas": int32(2),
					"maxReplicas": int32(4),
					"metrics":     hpaMetrics,
					"behaviour": map[string]interface{}{
						"scaleUp": scaleUpBehaviour,
					},
				}).
				MustBuild())
			if scenario.EnableLB {
				patches = append(patches, tests.NewJSONPatchBuilder().
					Add("/spec/requestsLoadBalancer/spec/replicaCount", 4).
					MustBuild())
				patches = append(patches, tests.NewJSONPatchBuilder().
					Add("/spec/requestsLoadBalancer/spec/hpa", map[string]interface{}{
						"minReplicas": int32(1),
						"maxReplicas": int32(4),
						"metrics":     hpaMetrics,
						"behaviour": map[string]interface{}{
							"scaleUp": scaleUpBehaviour,
							"scaleDown": map[string]interface{}{
								"stabilizationWindowSeconds": int32(30),
								"policies":                   scaleDownPolicy,
							},
						},
					}).
					MustBuild())
			}
		}

		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmClient, patches)
		By("VMCluster is available")

		if scenario.SetupFunc != nil {
			scenario.SetupFunc(ctx, kubeOpts, namespace)
		}

		k6Scenario := selectK6Scenario(scenario.K6Scenario, scenario.EnableHPA)
		const parallelism = 3

		var extraEnvVars map[string]string
		if scenario.ExtraEnvVarsFunc != nil {
			extraEnvVars = scenario.ExtraEnvVarsFunc(namespace)
		}
		err = install.RunK6Scenario(ctx, t, namespace, clusterName, k6Scenario, parallelism, scenario.ScenarioName, extraEnvVars)
		require.NoError(t, err)

		By("Waiting for K6 jobs to complete")
		k6WaitDuration := 15 * time.Minute
		if scenario.K6MaxDuration > 0 {
			k6WaitDuration = scenario.K6MaxDuration
		}
		install.WaitForK6JobsToComplete(ctx, t, namespace, scenarioName, parallelism, k6WaitDuration)

		tests.WaitForDataPropagation()

		checkMetric := func(purpose, query string) tests.ScannedMetric {
			By(purpose)
			timestamp := time.Now().Format(time.RFC3339)
			values, _, err := overwatch.QueryRange(ctx, query)
			require.NoError(t, err, "Failed to make a query %q at time %s", purpose, timestamp)

			matrix, ok := values.(model.Matrix)
			require.True(t, ok, "query %q returned %s instead of matrix", purpose, values.Type())
			require.NotEmpty(t, matrix, "query %q returned no series", purpose)
			samples := matrix[0].Values
			require.NotEmpty(t, samples, "query %q returned no samples", purpose)
			lastValue := samples[len(samples)-1].Value

			return tests.NewScannedMetric(t, lastValue, purpose,
				tests.MetricParameter{Name: "query", Value: query},
				tests.MetricParameter{Name: "timestamp", Value: timestamp},
				tests.MetricParameter{Name: "value", Value: fmt.Sprintf("%v", lastValue)},
			)
		}
		checkMetric(
			"No rows were ignored",
			fmt.Sprintf(`sum(vm_rows_ignored_total{namespace="%s"})`, namespace),
		).EqualTo(model.SampleValue(0))
		checkMetric(
			"No rows were invalid",
			fmt.Sprintf(`sum(vm_rows_invalid_total{namespace="%s"})`, namespace),
		).EqualTo(model.SampleValue(0))
		scenario.VerificationFunc(checkMetric, namespace, scenarioName)
	}

	DescribeTable("prw2-50vus-10mins load test",
		runLoadScenario,
		Entry("baseline", Label("id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"), LoadScenario{
			ScenarioName: "baseline",
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(70_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(70_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(13_000)

				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(1)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(60)
			},
		}),
		Entry("with VMStorage replica cycling", Label("id=b2c3d4e5-f6a7-8901-bcde-f12345678901"), LoadScenario{
			ScenarioName: "vmstorage-cycling",
			SetupFunc:    vmStorageCyclingSetupFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(20_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(22_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(12_000)

				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(100)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(100)
			},
		}),
		Entry("with NFS storage", Label("id=c3d4e5f6-a7b8-9012-cdef-123456789012"), LoadScenario{
			ScenarioName: "nfs-storage",
			PreInstallFunc: func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) []jsonpatch.Patch {
				// Deploy NFS server and get the StorageClass name that the static NFS
				// PersistentVolumes are registered under.
				scName := install.InstallNFSServer(ctx, t, kubeOpts, namespace)
				// Point vmstorage and vmselect volumeClaimTemplates at our NFS-backed
				// StorageClass. The operator creates PVCs normally; they bind to static PVs.
				patch := tests.NewJSONPatchBuilder().
					Add("/spec/vmstorage/storage/volumeClaimTemplate/spec/storageClassName", scName).
					Add("/spec/vmselect/storage/volumeClaimTemplate/spec/storageClassName", scName).
					MustBuild()
				return []jsonpatch.Patch{patch}
			},
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(70_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(70_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(13_000)

				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(35)
			},
		}),
		Entry("with OpenTelemetry ingestion", Label("id=d4e5f6a7-b8c9-0123-defa-234567890123"), LoadScenario{
			ScenarioName: "otlp",
			K6Scenario:   "otlp-50vus-10mins",
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"OTLP rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(70_000)
				checkMetric(
					"k6 OTLP insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(70_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(17_000)

				checkMetric(
					"k6 OTLP insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 OTLP insert requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(5)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(25)
			},
		}),
		Entry("HPA with load-balancers", Label("id=c3d4e5f6-a7b8-9012-cdef-123456789abc"), LoadScenario{
			ScenarioName: "hpa",
			EnableLB:     true,
			EnableHPA:    true,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(70_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(70_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(11_000)

				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(100)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(100)
			},
		}),
		// VMAgent ingestion path: k6 pushes to VMAgent which forwards to VMInsert,
		// verifying the full VMAgent→VMInsert→VMStorage pipeline end-to-end.
		Entry("with VMAgent ingestion", Label("id=e5f6a7b8-c9d0-1234-efab-345678901234"), LoadScenario{
			ScenarioName: "vmagent",
			SetupFunc:    vmAgentSetupFunc,
			ExtraEnvVarsFunc: func(ns string) map[string]string {
				return map[string]string{
					"VMINSERT_URL": fmt.Sprintf("http://%s/api/v1/write", consts.VMAgentNamespacedHost(ns)),
				}
			},
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted via VMAgent without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(70_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(70_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(13_000)

				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(5)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(60)
				// Verify VMAgent successfully forwarded rows — its sent counter should be non-zero.
				checkMetric(
					"VMAgent forwarded rows to VMInsert",
					fmt.Sprintf(`max_over_time(sum(vmagent_remotewrite_rows_pushed_after_relabel_total{namespace="%s"})[15m])`, namespace),
				).Greater(1_800_000)
			},
		}),
		// Slowness-based rerouting improvement.
		// One vmstorage node (pod-0) is slowed via 500ms network delay injected by Chaos Mesh.
		// The improved rerouting logic should detect the slowest node and reroute from it only,
		// without triggering a rerouting storm across the whole cluster.
		Entry("slowness rerouting", Label("id=a7f3c2e1-d4b5-4e89-9f01-2345678901ab"), LoadScenario{
			ScenarioName: "slowest-rerouting",
			SetupFunc:    vmStorageSlownessSetupFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(20_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(20_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(12_000)

				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable under slowness",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(100)
				checkMetric(
					"k6 read requests duration is acceptable under slowness",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(100)
				// Slowness on pod-0 should trigger rerouting — verify rows were rerouted.
				checkMetric(
					"Slow inserts were detected on the bottleneck node",
					fmt.Sprintf(`max_over_time(sum(vm_slow_row_inserts_total{namespace="%s"})[15m])`, namespace),
				).Greater(0)
				checkMetric(
					"Rows were rerouted away from the slow node",
					fmt.Sprintf(`max_over_time(sum(vm_rpc_rows_rerouted_to_here_total{namespace="%s", addr="vmstorage-vm-load-slowest-rerouting-1.vmstorage-vm-load-slowest-rerouting.vm-load-slowest-rerouting:8400"})[15m])`, namespace),
				).Greater(0)
			},
		}),
	)
})
