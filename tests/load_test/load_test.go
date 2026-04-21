package load_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"

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

type scannedMetric struct {
	t     require.TestingT
	value model.SampleValue
	query string
}

func (s scannedMetric) Greater(expected float64) {
	require.Greater(s.t, float64(s.value), expected, "\nquery: %s", s.query)
}

func (s scannedMetric) Less(expected float64) {
	require.Less(s.t, float64(s.value), expected, "\nquery: %s", s.query)
}

func (s scannedMetric) EqualTo(expected model.SampleValue) {
	require.Equal(s.t, expected, s.value, "\nquery: %s", s.query)
}

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
		Patches      []jsonpatch.Patch
		// BackgroundFunc, if non-nil, creates a background function using namespace-specific state.
		BackgroundFunc   func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc
		VerificationFunc func(checkMetric func(purpose, query string) scannedMetric, namespace, scenarioName string)
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

	requestsLoadBalancerPatch := tests.NewJSONPatchBuilder().
		Add("/spec/requestsLoadBalancer", map[string]string{}).
		Add("/spec/requestsLoadBalancer/enabled", true).
		Add("/spec/requestsLoadBalancer/spec", map[string]string{}).
		Add("/spec/requestsLoadBalancer/spec/replicaCount", 1).
		Add("/spec/requestsLoadBalancer/spec/resources", map[string]string{}).
		Add("/spec/requestsLoadBalancer/spec/resources/limits", map[string]string{}).
		Add("/spec/requestsLoadBalancer/spec/resources/limits/cpu", "250m").
		Add("/spec/requestsLoadBalancer/spec/resources/limits/memory", "500Mi").
		MustBuild()

	vmInsertCyclingBackgroundFunc := func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc {
		return func(cycleCtx context.Context) {
			install.WaitForVMClusterToBeOperational(cycleCtx, t, kubeOpts, namespace, vmClient)
			for cycleCtx.Err() == nil {
				install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleInsertReplicas(3))
				install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleInsertReplicas(2))
			}
		}
	}

	vmStorageCyclingBackgroundFunc := func(kubeOpts *k8s.KubectlOptions, vmClient vmclient.Interface, namespace string) backgroundFunc {
		return func(cycleCtx context.Context) {
			install.WaitForVMClusterToBeOperational(cycleCtx, t, kubeOpts, namespace, vmClient)
			for cycleCtx.Err() == nil {
				install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleStorageReplicas(3))
				install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, namespace, namespace, vmClient, scaleStorageReplicas(2))
			}
		}
	}

	runLoadScenario := func(ctx context.Context, scenario LoadScenario) {
		overwatch, err := tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		scenarioName := scenario.ScenarioName
		namespace := fmt.Sprintf("vm-load-%s", scenarioName)
		k6Namespace := fmt.Sprintf("k6-tests-%s", scenarioName)

		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		k6KubeOpts := k8s.NewKubectlOptions("", "", k6Namespace)

		defer func() {
			gather.VMAfterAll(ctx, t, consts.ResourceWaitTimeout, namespace)

			defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
			gather.K8sAfterAll(ctx, t, defaultKubeOpts, consts.ResourceWaitTimeout)

			// overwatchKubeOpts := k8s.NewKubectlOptions("", "", consts.OverwatchNamespace)
			// gather.RestartOverwatchInstance(ctx, t, overwatchKubeOpts)

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

		patches := scenario.Patches
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

		err = install.RunK6Scenario(ctx, t, k6Namespace, namespace, clusterName, k6Scenario, parallelism, scenario.ScenarioName)
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
		install.WaitForK6JobsToComplete(ctx, t, k6Namespace, scenarioName, parallelism)
		cancelCycle()
		wg.Wait()

		checkMetric := func(purpose, query string) scannedMetric {
			By(purpose)
			_, value, err := overwatch.VectorScan(ctx, query)
			require.NoError(t, err, "%s\nquery: %s\n", purpose, query)
			return scannedMetric{t: t, value: value, query: query}
		}

		checkMetric(
			"PRW v2 rows were inserted without errors",
			fmt.Sprintf(`sum(vm_rows_inserted_total{namespace="%s"})`, namespace),
		).Greater(190_000)
		checkMetric(
			"No rows were ignored",
			fmt.Sprintf(`sum(vm_rows_ignored_total{namespace="%s"})`, namespace),
		).EqualTo(model.SampleValue(0))
		checkMetric(
			"No rows were invalid",
			fmt.Sprintf(`sum(vm_rows_invalid_total{namespace="%s"})`, namespace),
		).EqualTo(model.SampleValue(0))

		checkMetric(
			"k6 insert requests were made",
			fmt.Sprintf(`sum(k6_http_reqs_total{scenario="insert", job_name=~"%s.*"})`, scenarioName),
		).Greater(1_200_000)
		checkMetric(
			"k6 read requests were made",
			fmt.Sprintf(`sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})`, scenarioName),
		).Greater(2_000)

		checkMetric(
			"k6 insert requests failure rate is acceptable",
			fmt.Sprintf(`max_over_time(sum(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"})[10m])`, scenario.ScenarioName),
		).Less(10)
		checkMetric(
			"k6 read requests failure rate is acceptable",
			fmt.Sprintf(`max_over_time(sum(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"})[10m])`, scenario.ScenarioName),
		).Less(10)
		checkMetric(
			"k6 insert requests duration is acceptable",
			fmt.Sprintf(`max_over_time(sum(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"})[10m])`, scenario.ScenarioName),
		).Less(5)
		checkMetric(
			"k6 read requests duration is acceptable",
			fmt.Sprintf(`max_over_time(sum(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"})[10m])`, scenario.ScenarioName),
		).Less(20)

		if scenario.VerificationFunc != nil {
			scenario.VerificationFunc(checkMetric, namespace, scenarioName)
		}
	}

	DescribeTable("prw2-50vus-10mins load test",
		runLoadScenario,
		Entry("baseline", Label("id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"), LoadScenario{
			ScenarioName: "baseline",
		}),
		Entry("with VMInsert replica cycling", Label("id=6bbeb19c-85bb-45df-8f1f-d95068bec025"), LoadScenario{
			ScenarioName:   "vminsert-cycling",
			BackgroundFunc: vmInsertCyclingBackgroundFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) scannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`sum(vm_rows_inserted_total{namespace="%s"})`, namespace),
				).Greater(200_000)
			},
		}),
		Entry("with VMStorage replica cycling", Label("id=b2c3d4e5-f6a7-8901-bcde-f12345678901"), LoadScenario{
			ScenarioName:   "vmstorage-cycling",
			BackgroundFunc: vmStorageCyclingBackgroundFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) scannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`sum(vm_rows_inserted_total{namespace="%s"})`, namespace),
				).Greater(200_000)
			},
		}),

		Entry("baseline load-balancers", Label("id=be8591e4-e072-4aec-b19d-b03f76229370"), LoadScenario{
			ScenarioName: "baseline-lb",
			Patches:      []jsonpatch.Patch{requestsLoadBalancerPatch},
			VerificationFunc: func(checkMetric func(purpose, query string) scannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`sum(vm_rows_inserted_total{namespace="%s"})`, namespace),
				).Greater(200_000)
			},
		}),
		Entry("with VMInsert replica cycling behind load-balancers", Label("id=5e7667e0-e0a7-4467-9c71-6aaa109a5e75"), LoadScenario{
			ScenarioName:   "vminsert-cycling-clusterlb",
			Patches:        []jsonpatch.Patch{requestsLoadBalancerPatch},
			BackgroundFunc: vmInsertCyclingBackgroundFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) scannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`sum(vm_rows_inserted_total{namespace="%s"})`, namespace),
				).Greater(200_000)
			},
		}),
		Entry("with VMStorage replica cycling behind load-balancers", Label("id=f43441ea-f348-496f-94ff-65f2c4991a24"), LoadScenario{
			ScenarioName:   "vmstorage-cycling-clusterlb",
			Patches:        []jsonpatch.Patch{requestsLoadBalancerPatch},
			BackgroundFunc: vmStorageCyclingBackgroundFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) scannedMetric, namespace, scenarioName string) {
				checkMetric(
					"PRW v2 rows were inserted without errors",
					fmt.Sprintf(`sum(vm_rows_inserted_total{namespace="%s"})`, namespace),
				).Greater(200_000)
			},
		}),
	)
})
