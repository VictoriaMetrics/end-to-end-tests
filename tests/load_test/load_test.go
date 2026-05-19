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

	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/gather"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

// backgroundFunc runs concurrently with a k6 scenario.
// It receives a context that is cancelled once the k6 jobs complete.
type backgroundFunc func(ctx context.Context)

func TestLoadTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Load test Suite", suiteConfig, reporterConfig)
}

var (
	t terratesting.TestingT
)

// Install shared infra once on process 1; all processes receive their own t.
var _ = SynchronizedAfterSuite(
	func(ctx context.Context) {},
	func(ctx context.Context) {
		t := tests.GetT()
		overwatchKubeOpts := k8s.NewKubectlOptions("", "", consts.OverwatchNamespace)
		gather.RestartOverwatchInstance(ctx, t, overwatchKubeOpts)
	},
)

var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t := tests.GetT()
		install.DiscoverIngressHost(ctx, t)
		install.InstallVMGather(ctx, t)
		install.InstallVMK8StackWithHelm(
			ctx,
			consts.VMK8sStackChart,
			consts.SmokeValuesFile(),
			t,
			consts.DefaultVMNamespace,
			consts.DefaultReleaseName,
		)
		install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)

		// Remove the stock helm-managed VMCluster; each load test creates its own.
		defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.DeleteVMCluster(t, defaultKubeOpts, consts.DefaultReleaseName)

		// Install k6 operator
		install.InstallK6(ctx, t, consts.K6OperatorNamespace)

		// Install Chaos Mesh
		chaosCfg := tests.DefaultChaosMeshConfig()
		install.InstallChaosMesh(ctx, chaosCfg.HelmChart, chaosCfg.ValuesFile, t, chaosCfg.Namespace, chaosCfg.ReleaseName)

		// Install KEDA operator
		install.InstallKEDA(ctx, t, consts.KEDANamespace)

		// Add custom alert rules
		install.AddCustomAlertRules(ctx, t, consts.DefaultVMNamespace)
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = Describe("Load tests", Label("load-test"), func() {

	// LoadScenario holds configuration for a single load test run.
	type LoadScenario struct {
		ScenarioName string
		Patches      []jsonpatch.Patch
		EnableLB     bool
		// SetupFunc, if non-nil, is called after the namespace is created but before VMCluster
		// installation. It can be used to provision supporting infrastructure (e.g. NFS server)
		// and returns additional patches to apply to the VMCluster manifest.
		SetupFunc func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) []jsonpatch.Patch
		// EnableKEDA, if true, installs KEDA ScaledObjects for every VMCluster component
		// (vminsert, vmselect, vmstorage) and for the requestsLoadBalancer VMAuth deployment.
		EnableKEDA bool
		// EnableHPA, if true, installs a Kubernetes HorizontalPodAutoscaler targeting the
		// requestsLoadBalancer VMAuth Deployment (vmauth-<clusterName>). Requires EnableLB.
		EnableHPA bool
		// BackgroundFunc, if non-nil, creates a background function using namespace-specific state.
		BackgroundFunc   func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc
		VerificationFunc func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string)
	}

	vmStorageCyclingBackgroundFunc := func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc {
		return func(cycleCtx context.Context) {
			// Restart vmstorage pods one by one with a 90s delay between them using Chaos Mesh.
			// The workflow kills pod-0, waits 90s, then kills pod-1 — exactly once.
			install.RunChaosScenario(cycleCtx, t, namespace, "pods", "vmstorage-pod-restart-cycling", "workflows")
		}
	}

	runLoadScenario := func(ctx context.Context, scenario LoadScenario) {
		overwatch, err := tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		scenarioName := scenario.ScenarioName
		namespace := fmt.Sprintf("vm-load-%s", scenarioName)

		kubeOpts := k8s.NewKubectlOptions("", "", namespace)

		DeferCleanup(func(ctx context.Context) {
			gather.VMAfterAll(ctx, t, consts.ResourceWaitTimeout)

			if CurrentSpecReport().Failed() {
				defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
				gather.K8sAfterAll(ctx, t, defaultKubeOpts, consts.ResourceWaitTimeout)
			}

			install.DeleteVMCluster(t, kubeOpts, namespace)
			if scenario.SetupFunc != nil {
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
		if scenario.SetupFunc != nil {
			extraPatches := scenario.SetupFunc(ctx, kubeOpts, namespace)
			patches = append(patches, extraPatches...)
		}
		for _, component := range []string{"vminsert", "vmselect", "vmstorage"} {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/metadata/name", clusterName).
				Add(fmt.Sprintf("/spec/%s/affinity", component), affinity).
				MustBuild())
		}
		lbCPULimit := "250m"
		lbMemLimit := "500Mi"
		if scenario.EnableKEDA {
			lbCPULimit = "125m"
			lbMemLimit = "250Mi"
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

		// Nodes are dedicated (4 CPU / 13.3Gi allocatable). DaemonSets consume ~258m CPU,
		// monitoring pods run on non-monitoring nodes, LB keeps 250m CPU / 500Mi mem,
		// leaving ~3492m CPU and ~12.8Gi for 6 cluster pods.
		// KEDA halves per-pod resources to allow more instances to scale up.
		type componentResources struct{ cpuReq, memReq, memLimit string }
		componentResourceMap := map[string]componentResources{
			"vminsert":  {"400m", "500Mi", "1Gi"},
			"vmselect":  {"400m", "1Gi", "2Gi"},
			"vmstorage": {"600m", "2Gi", "3Gi"},
		}
		if scenario.EnableKEDA {
			componentResourceMap = map[string]componentResources{
				"vminsert":  {"200m", "250Mi", "512Mi"},
				"vmselect":  {"200m", "512Mi", "1Gi"},
				"vmstorage": {"300m", "1Gi", "1536Mi"},
			}
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

		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmClient, patches)
		By("VMCluster is available")

		if scenario.EnableKEDA {
			install.InstallKEDAScaledObjects(ctx, t, kubeOpts, namespace, clusterName)
			By("KEDA ScaledObjects installed for all VMCluster components")
		}
		if scenario.EnableHPA {
			install.InstallHPAForVMCluster(ctx, t, kubeOpts, namespace, clusterName)
			By("HPAs installed for VMCluster components (vminsert, vmselect, vmstorage)")
			if scenario.EnableLB {
				install.InstallHPAForLB(ctx, t, kubeOpts, namespace, clusterName)
				By("HPA installed for requestsLoadBalancer")
			}
		}

		var k6Scenario = "prw2-50vus-10mins"
		if scenario.EnableKEDA {
			k6Scenario = "ramping-metrics"
		}
		const parallelism = 3

		err = install.RunK6Scenario(ctx, t, namespace, clusterName, k6Scenario, parallelism, scenario.ScenarioName, nil)
		require.NoError(t, err)

		cycleCtx, cancelCycle := context.WithCancel(ctx)
		DeferCleanup(cancelCycle)
		var wg sync.WaitGroup
		if scenario.BackgroundFunc != nil {
			fn := scenario.BackgroundFunc(kubeOpts, vmClient, namespace)
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				fn(cycleCtx)
			}()
		}

		By("Waiting for K6 jobs to complete")
		install.WaitForK6JobsToComplete(ctx, t, namespace, scenarioName, parallelism)
		cancelCycle()
		wg.Wait()

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
			ScenarioName: "nolb-baseline",
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
				).Greater(20_000)

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
				).Less(1)
			},
		}),
		Entry("with VMStorage replica cycling", Label("id=b2c3d4e5-f6a7-8901-bcde-f12345678901"), LoadScenario{
			ScenarioName:   "nolb-vmstorage-cycling",
			BackgroundFunc: vmStorageCyclingBackgroundFunc,
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
				).Greater(14_000)

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
		Entry("baseline load-balancers", Label("id=be8591e4-e072-4aec-b19d-b03f76229370"), LoadScenario{
			ScenarioName: "lb-baseline",
			EnableLB:     true,
			EnableHPA:    false,
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
				).Greater(20_000)

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
			ScenarioName: "nolb-nfs-storage",
			SetupFunc: func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) []jsonpatch.Patch {
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
				).Greater(20_000)

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
				).Less(1)
			},
		}),
		Entry("with VMStorage replica cycling behind load-balancers", Label("id=f43441ea-f348-496f-94ff-65f2c4991a24"), LoadScenario{
			ScenarioName:   "lb-vmstorage-cycling",
			EnableLB:       true,
			EnableHPA:      false,
			BackgroundFunc: vmStorageCyclingBackgroundFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`max_over_time(sum(vm_rows_inserted_total{namespace="%s"})[15m])`, namespace),
				).Greater(13_000)
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(12_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(10_000)

				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[10m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[10m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[10m]))`, scenarioName),
				).Less(100)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[10m]))`, scenarioName),
				).Less(100)
			},
		}),
		FEntry("baseline load-balancers with KEDA autoscaling", Label("id=c3d4e5f6-a7b8-9012-cdef-123456789abc"), LoadScenario{
			ScenarioName: "lb-keda-baseline",
			EnableLB:     true,
			EnableKEDA:   true,
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
				).Greater(20_000)

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
