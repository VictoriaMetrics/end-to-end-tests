package load_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	jsonpatch "github.com/evanphx/json-patch/v5"

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
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t := tests.GetT()
		install.DiscoverIngressHost(ctx, t)
		install.InstallVMGather(t)
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
		// BackgroundFunc, if non-nil, creates a background function using namespace-specific state.
		BackgroundFunc func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc
	}

	scaleStorageReplicas := func(replicas int32) func(*vmv1beta1.VMClusterSpec) {
		return func(spec *vmv1beta1.VMClusterSpec) {
			spec.VMStorage.ReplicaCount = &replicas
		}
	}

	scaleInsertReplicas := func(replicas int32) func(*vmv1beta1.VMClusterSpec) {
		return func(spec *vmv1beta1.VMClusterSpec) {
			spec.VMInsert.ReplicaCount = &replicas
		}
	}

	runLoadScenario := func(ctx context.Context, scenario LoadScenario) {
		overwatch, err := tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		namespace := fmt.Sprintf("vm-load-%s", scenario.ScenarioName)
		k6Namespace := fmt.Sprintf("k6-tests-%s", scenario.ScenarioName)

		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		k6KubeOpts := k8s.NewKubectlOptions("", "", k6Namespace)

		defer func() {
			gather.VMAfterAll(ctx, t, consts.ResourceWaitTimeout, namespace)

			defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
			gather.K8sAfterAll(ctx, t, defaultKubeOpts, consts.ResourceWaitTimeout)

			overwatchKubeOpts := k8s.NewKubectlOptions("", "", consts.OverwatchNamespace)
			gather.RestartOverwatchInstance(ctx, t, overwatchKubeOpts)

			install.DeleteVMCluster(t, kubeOpts, namespace)
			tests.CleanupNamespace(t, kubeOpts, namespace)
			tests.CleanupNamespace(t, k6KubeOpts, k6Namespace)
		}()

		tests.CleanupNamespace(t, kubeOpts, namespace)
		tests.EnsureNamespaceExists(t, kubeOpts, namespace)
		k8s.RunKubectl(t, kubeOpts, "label", "namespace", namespace, "vm-load-test=true", "--overwrite")

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

		patches := []jsonpatch.Patch{}
		for _, component := range []string{"vminsert", "vmselect", "vmstorage"} {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/metadata/name", clusterName).
				Add(fmt.Sprintf("/spec/%s/affinity", component), affinity).
				MustBuild())
		}

		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmClient, patches)
		By("VMCluster is available")

		// Prepare k6 namespace
		tests.EnsureNamespaceExists(t, k6KubeOpts, k6Namespace)

		const k6Scenario = "prw2-50vus-10mins"
		const parallelism = 3

		err = install.RunK6Scenario(ctx, t, k6Namespace, namespace, clusterName, k6Scenario, parallelism)
		require.NoError(t, err)

		cycleCtx, cancelCycle := context.WithCancel(ctx)
		defer cancelCycle()
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
		install.WaitForK6JobsToComplete(ctx, t, k6Namespace, k6Scenario, parallelism)
		cancelCycle()
		wg.Wait()

		By("PRW v2 rows were inserted without errors")
		_, value, err := overwatch.VectorScan(ctx, fmt.Sprintf("sum(vm_rows_inserted_total{namespace=\"%s\"})", namespace))
		require.NoError(t, err)
		require.Greater(t, value, float64(200_000))

		By("No rows were ignored")
		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf("sum(vm_rows_ignored_total{namespace=\"%s\"})", namespace))
		require.NoError(t, err)
		require.Equal(t, model.SampleValue(0), value)

		By("No rows were invalid")
		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf("sum(vm_rows_invalid_total{namespace=\"%s\"})", namespace))
		require.NoError(t, err)
		require.Equal(t, model.SampleValue(0), value)

		By("k6 insert and read requests were made")
		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf(`sum(k6_http_reqs_total{scenario="insert", name=~"%s"})`, namespace))
		require.NoError(t, err)
		require.Greater(t, value, float64(1_200_000))

		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf(`sum(k6_http_reqs_total{scenario="read", name=~"%s"})`, namespace))
		require.NoError(t, err)
		require.Greater(t, value, float64(2_000))
		// require.Greater(t, value, float64(10_000))

		By("k6 insert requests failure rate is acceptable")
		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf(`max_over_time(sum(k6_http_req_failed_rate{scenario="insert", name=~"%s"})[10m])`, namespace))
		require.NoError(t, err)
		require.Less(t, value, float64(10))
		//require.Equal(t, model.SampleValue(0), value)

		By("k6 read requests failure rate is acceptable")
		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf(`max_over_time(sum(k6_http_req_failed_rate{scenario="read", name=~"%s"})[10m])`, namespace))
		require.NoError(t, err)
		require.Less(t, value, float64(10))
		//require.Equal(t, model.SampleValue(0), value)

		By("k6 insert requests duration is acceptable")
		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf(`max_over_time(sum(k6_http_req_duration_p95{scenario="insert", name=~"%s"})[10m])`, namespace))
		require.NoError(t, err)
		require.Less(t, value, float64(5))

		By("k6 read requests duration is acceptable")
		_, value, err = overwatch.VectorScan(ctx, fmt.Sprintf(`max_over_time(sum(k6_http_req_duration_p95{scenario="read", name=~"%s"})[10m])`, namespace))
		require.NoError(t, err)
		require.Less(t, value, float64(20))
	}

	DescribeTable("prw2-50vus-10mins load test",
		func(ctx context.Context, scenario LoadScenario) {
			runLoadScenario(ctx, scenario)
		},
		Entry("baseline", Label("id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"), LoadScenario{
			ScenarioName: "baseline",
		}),
		Entry("with VMInsert replica cycling", Label("id=6bbeb19c-85bb-45df-8f1f-d95068bec025"), LoadScenario{
			ScenarioName: "vminsert-cycling",
			BackgroundFunc: func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc {
				return func(cycleCtx context.Context) {
					install.WaitForVMClusterToBeOperational(cycleCtx, t, kubeOpts, namespace, vmClient)
					for cycleCtx.Err() == nil {
						install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleInsertReplicas(3))
						install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleInsertReplicas(2))
					}
				}
			},
		}),
		Entry("with VMStorage replica cycling", Label("id=b2c3d4e5-f6a7-8901-bcde-f12345678901"), LoadScenario{
			ScenarioName: "vmstorage-cycling",
			BackgroundFunc: func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc {
				return func(cycleCtx context.Context) {
					install.WaitForVMClusterToBeOperational(cycleCtx, t, kubeOpts, namespace, vmClient)
					for cycleCtx.Err() == nil {
						install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleStorageReplicas(3))
						install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleStorageReplicas(2))
					}
				}
			},
		}),
	)
})
