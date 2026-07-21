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
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
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

func selectK6Scenario(k6Scenario string, enableHPA, enableVPA bool) string {
	if enableHPA || enableVPA {
		return "ramping-metrics"
	}
	if k6Scenario != "" {
		return k6Scenario
	}
	return "prw2-50vus-10mins"
}

func k6CompletedReadRequestsQuery(scenarioName string) string {
	return fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"^%s.*$"})[30m:])`, scenarioName)
}

func waitForK6MetricsScraped(ctx context.Context, t terratesting.TestingT, overwatch promquery.PrometheusClient, scenarioName string) {
	require.Eventually(t, func() bool {
		values, _, err := overwatch.QueryRange(ctx, fmt.Sprintf(`sum(k6_http_reqs_total{job_name=~"^%s.*$"})`, scenarioName))
		if err != nil {
			return false
		}
		matrix, ok := values.(model.Matrix)
		return ok && len(matrix) > 0 && len(matrix[0].Values) > 0
	}, 2*consts.DataPropagationDelay, consts.PollingInterval, "k6 metrics for %s were not scraped", scenarioName)
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
		// EnableVPA, if true, configures VerticalPodAutoscalers on vminsert, vmselect,
		// and vmstorage via the VMCluster spec. Requires VM_VPA_API_ENABLED to be set on
		// the operator. VPA adjusts pod resource requests in response to actual usage;
		// the ramping-metrics k6 scenario is used to generate enough load for VPA to act.
		EnableVPA bool
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

	// vmStorageSlownessSetupFunc simulates a slow vmstorage-0 by injecting 1s network
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
		namespace := tests.RandomNamespace(fmt.Sprintf("vm-load-%s", scenarioName))

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

		if scenario.EnableVPA {
			_, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts,
				"get", "crd", "verticalpodautoscalers.autoscaling.k8s.io")
			if err != nil {
				// VPA CRDs not yet installed; apply them and wait for establishment.
				install.KubectlApply(ctx, t, kubeOpts, consts.VPACRDsYaml())
				k8s.RunKubectlContext(t, ctx, kubeOpts,
					"wait", "--for=condition=Established",
					"crd", "verticalpodautoscalers.autoscaling.k8s.io",
					"verticalpodautoscalercheckpoints.autoscaling.k8s.io",
					"--timeout=60s",
				)
			}
			install.SetVMOperatorEnv(ctx, t, consts.DefaultVMNamespace, "VM_VPA_API_ENABLED", "true")

			// Configure VPAs via VMCluster spec so the operator manages them natively.
			// updateMode=Auto: VPA applies resource recommendations automatically.
			// containerPolicies define per-container resource bounds.
			vpaSpec := map[string]interface{}{
				"updatePolicy": map[string]interface{}{
					"updateMode": "Auto",
				},
				"resourcePolicy": map[string]interface{}{
					"containerPolicies": []map[string]interface{}{
						{
							"containerName": "vminsert",
							"minAllowed": map[string]string{
								"cpu":    "100m",
								"memory": "128Mi",
							},
							"maxAllowed": map[string]string{
								"cpu":    "2",
								"memory": "2Gi",
							},
						},
					},
				},
			}
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/vminsert/vpa", vpaSpec).
				MustBuild())

			vpaSpec["resourcePolicy"] = map[string]interface{}{
				"containerPolicies": []map[string]interface{}{
					{
						"containerName": "vmselect",
						"minAllowed": map[string]string{
							"cpu":    "100m",
							"memory": "256Mi",
						},
						"maxAllowed": map[string]string{
							"cpu":    "2",
							"memory": "4Gi",
						},
					},
				},
			}
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/vmselect/vpa", vpaSpec).
				MustBuild())

			vpaSpec["resourcePolicy"] = map[string]interface{}{
				"containerPolicies": []map[string]interface{}{
					{
						"containerName": "vmstorage",
						"minAllowed": map[string]string{
							"cpu":    "200m",
							"memory": "512Mi",
						},
						"maxAllowed": map[string]string{
							"cpu":    "2",
							"memory": "6Gi",
						},
					},
				},
			}
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/vmstorage/vpa", vpaSpec).
				MustBuild())
		}

		install.InstallVMClusterWithOperationalTimeout(ctx, t, kubeOpts, namespace, vmClient, patches, consts.PollingTimeout)
		By("VMCluster is available")

		if scenario.SetupFunc != nil {
			scenario.SetupFunc(ctx, kubeOpts, namespace)
		}

		k6Scenario := selectK6Scenario(scenario.K6Scenario, scenario.EnableHPA, scenario.EnableVPA)
		const parallelism = 3

		var extraEnvVars map[string]string
		if scenario.ExtraEnvVarsFunc != nil {
			extraEnvVars = scenario.ExtraEnvVarsFunc(namespace)
		}
		metricStart := time.Now()
		err = install.RunK6Scenario(ctx, t, namespace, clusterName, k6Scenario, parallelism, scenario.ScenarioName, extraEnvVars)
		require.NoError(t, err)

		By("Waiting for K6 jobs to complete")
		k6WaitDuration := 15 * time.Minute
		if scenario.K6MaxDuration > 0 {
			k6WaitDuration = scenario.K6MaxDuration
		}
		install.WaitForK6JobsToComplete(ctx, t, namespace, scenarioName, parallelism, k6WaitDuration)
		metricEnd := time.Now()
		waitForK6MetricsScraped(ctx, t, overwatch, scenarioName)

		tests.WaitForDataPropagation()

		checkMetric := func(purpose, query string) tests.ScannedMetric {
			By(purpose)
			timestamp := time.Now().Format(time.RFC3339)
			values, _, err := overwatch.QueryRangeAt(ctx, query, metricStart, metricEnd)
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
				tests.MetricParameter{Name: "start", Value: metricStart.Format(time.RFC3339)},
				tests.MetricParameter{Name: "end", Value: metricEnd.Format(time.RFC3339)},
				tests.MetricParameter{Name: "value", Value: fmt.Sprintf("%v", lastValue)},
			)
		}
		checkMetric(
			"No rows were invalid",
			fmt.Sprintf(`sum(vm_rows_invalid_total{namespace="%s"})`, namespace),
		).EqualTo(model.SampleValue(0))
		checkMetric(
			"k6 read workload did not drop scheduled iterations",
			fmt.Sprintf(`sum(max_over_time(k6_dropped_iterations_total{scenario="read", job_name=~"^%s.*$"}[15m])) or 0`, scenarioName),
		).EqualTo(model.SampleValue(0))
		scenario.VerificationFunc(checkMetric, namespace, scenarioName)
	}

	DescribeTable("prw2-50vus-10mins load test",
		runLoadScenario,
		// Baseline: steady PRW v2 load (5000 inserts/s, 50 read VUs) against a stock 3-replica
		// VMCluster for 5 minutes. No chaos. Establishes the performance floor: row insertion
		// throughput, k6 request counts, failure rates, and p95 latency that all other tests
		// compare against.
		Entry("baseline", Label("id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "baseline",
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(35_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(35_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(2_500)

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
		// VMStorage replica cycling: Chaos Mesh kills vmstorage pod-0, waits 90 s, then kills
		// pod-1 — one restart cycle within a 10-minute deadline. PRW v2 load runs for 5 minutes.
		// Validates that vminsert's rerouting and persistent-queue mechanisms absorb storage
		// interruptions with acceptable failure rates and no data loss.
		Entry("with VMStorage replica cycling", Label("id=b2c3d4e5-f6a7-8901-bcde-f12345678901"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "vmstorage-cycling",
			SetupFunc:    vmStorageCyclingSetupFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(10_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(11_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(4_500)

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
		// NFS storage: deploys an in-cluster NFS server and binds vmstorage and vmselect
		// PersistentVolumes to NFS-backed StorageClasses before the VMCluster starts. PRW v2
		// load then runs for 5 minutes. Validates that network-attached storage does not
		// degrade throughput beyond acceptable p95 latency and failure-rate thresholds.
		Entry("with NFS storage", Label("id=c3d4e5f6-a7b8-9012-cdef-123456789012"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "nfs-storage",
			ExtraEnvVarsFunc: func(namespace string) map[string]string {
				return map[string]string{
					"K6_INSERT_RATE": "500",
					"K6_READ_VUS":    "10",
				}
			},
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
				).Greater(35_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(35_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(6_500)

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
				).Less(15)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(55)
			},
		}),
		// OpenTelemetry ingestion: runs the same steady load as the baseline but sends data
		// via the OTLP protobuf endpoint (/opentelemetry/v1/metrics) instead of PRW v2.
		// Validates that the OTLP translation layer sustains equivalent throughput and that
		// failure rates and p95 latencies stay within acceptable bounds for 5 minutes.
		Entry("with OpenTelemetry ingestion", Label("id=d4e5f6a7-b8c9-0123-defa-234567890123"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "otlp",
			K6Scenario:   "otlp-50vus-10mins",
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"OTLP rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(35_000)
				checkMetric(
					"k6 OTLP insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(35_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(7_500)

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
				).Less(70)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(25)
			},
		}),
		// HPA with load-balancers: VMCluster is deployed with a VMAuth requests load-balancer
		// and HorizontalPodAutoscalers on vminsert, vmselect, and vmstorage (1–4 replicas each,
		// triggered at 50% CPU / 80% memory). The ramping-metrics k6 scenario ramps insert
		// rate from 0 to 50k/s over 3.5 minutes then back to 0. Validates that the HPA scales
		// pods up under load and the LB distributes traffic without errors.
		Entry("HPA with load-balancers", Label("id=c3d4e5f6-a7b8-9012-cdef-123456789abc"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "hpa",
			EnableLB:     true,
			EnableHPA:    true,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(35_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(35_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(3_500)

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
		// VMAgent ingestion: deploys a VMAgent in the test namespace configured to forward
		// all remote-write data to the local VMCluster's vminsert. k6 targets VMAgent's
		// /api/v1/write endpoint instead of VMInsert directly, exercising the full
		// VMAgent→VMInsert→VMStorage pipeline. Validates end-to-end throughput, failure
		// rates, p95 latency, and that VMAgent's remotewrite sent counter is non-zero.
		Entry("with VMAgent ingestion", Label("id=e5f6a7b8-c9d0-1234-efab-345678901234"), SpecTimeout(35*time.Minute), LoadScenario{
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
				).Greater(35_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(35_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(6_000)

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
				// Verify VMAgent successfully forwarded k6 rows. Keep the threshold tied to the
				// k6 insert request lower bound; this VMAgent no longer forwards unrelated
				// monitoring scrapes after switching to a minimal spec.
				checkMetric(
					"VMAgent forwarded rows to VMInsert",
					fmt.Sprintf(`max_over_time(sum(vmagent_remotewrite_rows_pushed_after_relabel_total{namespace="%s"})[15m])`, namespace),
				).Greater(250_000)
			},
		}),
		// Slowness-based rerouting (PR #9945): Chaos Mesh injects 1s ± 100 ms network
		// delay on all vminsert→vmstorage pod-0 connections for 8 minutes. The improved
		// rerouting logic should detect the single slowest node and reroute only from it,
		// avoiding a rerouting storm across the cluster. Validates that slow inserts and
		// rerouted rows counters are non-zero while overall failure rates stay acceptable.
		Entry("slowness rerouting", Label("id=a7f3c2e1-d4b5-4e89-9f01-2345678901ab"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "slowest-rerouting",
			// High-throughput variant: each k6 request writes K6_BATCH_SIZE=500 timeseries so
			// that the per-storage-node send buffer in vminsert fills to >=1MB within seconds
			// of the chaos starting. Rerouting is only attempted once the buffer is full.
			ExtraEnvVarsFunc: func(namespace string) map[string]string {
				return map[string]string{"K6_BATCH_SIZE": "500"}
			},
			Patches: []jsonpatch.Patch{
				// Enable slowness-based rerouting: disabled by default since v1.40.
				// Raise insert concurrency so the test exercises vminsert→vmstorage
				// rerouting instead of saturating the vminsert frontdoor queue first.
				tests.NewJSONPatchBuilder().
					Add("/spec/vminsert/extraArgs/disableRerouting", "false").
					Add("/spec/vminsert/extraArgs/maxConcurrentInserts", "64").
					MustBuild(),
				// replicationFactor must be 1 (< vmstorage count) so vminsert has
				// somewhere to reroute slow-node rows. With replicationFactor==2 and
				// 2 storages every row already goes to both nodes, making rerouting
				// a no-op.
				tests.NewJSONPatchBuilder().
					Replace("/spec/replicationFactor", 1).
					MustBuild(),
				// Keep enough storage nodes for p90 saturation guard added in v1.146.
				tests.NewJSONPatchBuilder().
					Replace("/spec/vmstorage/replicaCount", 6).
					MustBuild(),
			},
			SetupFunc: vmStorageSlownessSetupFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(9_500)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(8_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(6_000)

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
				// Rerouting label semantics differ between counters: addr can point to either
				// the source or destination storage. The scenario namespace is isolated, so
				// any rerouted rows here prove slow-node rerouting happened.
				checkMetric(
					"Rows were rerouted away from the slow node",
					fmt.Sprintf(`max_over_time((sum(vm_rpc_rows_rerouted_to_here_total{namespace="%s"}) + sum(vm_rpc_rows_rerouted_from_here_total{namespace="%s"}))[15m])`, namespace, namespace),
				).Greater(0)
			},
		}),
		// Slow/idle clients occupy VMAgent insert slots, blocking normal remote-write traffic.
		// VMAgent is configured with -maxConcurrentInserts=10. Slot-occupier VUs (14 concurrent)
		// send large 8k-row batches back-to-back during the pressure window (1–4 min), exhausting
		// the slot pool. Normal clients (300 req/s) should observe latency spikes and/or 429 errors
		// during that window, then recover once slot-occupiers stop (4–5 min).
		Entry("VMAgent slow-client slot exhaustion", Label("id=b1c2d3e4-f5a6-7890-bcde-f12345678901"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "vmagent-slow-clients",
			K6Scenario:   "vmagent-slow-clients",
			SetupFunc: func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) {
				vmClient := install.GetVMClient(t, kubeOpts)
				vminsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write",
					consts.GetVMInsertSvc(namespace, namespace))
				patches := []jsonpatch.Patch{
					// Forward all received data to the local VMCluster.
					tests.NewJSONPatchBuilder().
						Add("/spec/remoteWrite", []map[string]string{{"url": vminsertURL}}).
						MustBuild(),
					// Cap insert concurrency and disable queueing so slots immediately return
					// 503 errors when exhausted — enabling normal inserts to observe failures.
					tests.NewJSONPatchBuilder().
						Add("/spec/extraArgs", map[string]string{
							"maxConcurrentInserts":    "10",
							"insert.maxQueueDuration": "1ms",
						}).
						MustBuild(),
				}
				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmClient, patches)
			},
			ExtraEnvVarsFunc: func(ns string) map[string]string {
				return map[string]string{
					"VMINSERT_URL": fmt.Sprintf("http://%s/api/v1/write", consts.VMAgentNamespacedHost(ns)),
				}
			},
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				// Normal inserts must reach VMAgent during baseline (0–1 min).
				checkMetric(
					"Normal inserts reached VMAgent during baseline",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(5_000)
				// During the pressure window (1–5 min) slot-occupiers should have triggered
				// measurable errors or latency on the normal_insert scenario.
				checkMetric(
					"Normal insert failure rate spiked during slot pressure",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="normal_insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Greater(0)
				// After slot-occupiers stop (4–5 min) the failure rate must recover.
				checkMetric(
					"Normal insert failure rate recovered after slot-occupiers stopped",
					fmt.Sprintf(`min_over_time(max(k6_http_req_failed_rate{scenario="normal_insert", job_name=~"%s.*"})[3m:15s])`, scenarioName),
				).Less(5)
				// VMAgent must have forwarded rows downstream throughout the test.
				checkMetric(
					"VMAgent forwarded rows to VMInsert",
					fmt.Sprintf(`max_over_time(sum(vmagent_remotewrite_rows_pushed_after_relabel_total{namespace="%s"})[15m])`, namespace),
				).Greater(1_250_000)
				// No rows were ignored
				checkMetric(
					"No rows were ignored",
					fmt.Sprintf(`sum(vm_rows_ignored_total{namespace="%s"})`, namespace),
				).EqualTo(model.SampleValue(0))
			},
		}),
		// VPA load test: VMCluster is deployed with VerticalPodAutoscalers on vminsert,
		// vmselect, and vmstorage (updateMode=Auto). The ramping-metrics k6 scenario ramps
		// insert rate from 0 to 50k/s over 3.5 minutes then back to 0. Validates that VPA
		// objects are created and that inserts succeed under ramping load.
		// Requires VM_VPA_API_ENABLED=true on the operator and VPA CRDs installed.
		Entry("VPA with ramping load", Label("id=vpa-load-01"), SpecTimeout(35*time.Minute), LoadScenario{
			ScenarioName: "vpa",
			EnableVPA:    true,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(35_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(35_000)
				checkMetric(
					"k6 read requests were made",
					k6CompletedReadRequestsQuery(scenarioName),
				).Greater(3_500)
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
	)
})
