package load_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	jsonpatch "github.com/evanphx/json-patch/v5"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/gather"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

// backgroundFunc is a function run concurrently with a k6 scenario.
// It receives a context that is cancelled once the k6 jobs complete.
type backgroundFunc func(ctx context.Context)

func TestLoadTestsTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Load test Suite", suiteConfig, reporterConfig)
}

var _ = Describe("Load tests", Ordered, ContinueOnFailure, Label("load-test"), func() {
	ctx := context.Background()
	t := tests.GetT()

	var overwatch promquery.PrometheusClient

	BeforeAll(func() {
		install.DiscoverIngressHost(ctx, t)
		install.InstallVMGather(t)
		install.InstallVMK8StackWithHelm(
			ctx,
			consts.VMK8sStackChart,
			consts.SmokeValuesFile,
			t,
			consts.DefaultVMNamespace,
			consts.DefaultReleaseName,
		)
		install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)

		var err error
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		// Install k6 operator
		install.InstallK6(ctx, t, consts.K6OperatorNamespace)

		// Remove the stock helm-managed VMCluster; load tests use their own in consts.LoadTestVMNamespace.
		defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.DeleteVMCluster(t, defaultKubeOpts, consts.DefaultReleaseName)

		// Create a fresh namespace and install a dedicated VMCluster for load tests.
		loadTestKubeOpts := k8s.NewKubectlOptions("", "", consts.LoadTestVMNamespace)
		tests.EnsureNamespaceExists(t, loadTestKubeOpts, consts.LoadTestVMNamespace)
		vmClient := install.GetVMClient(t, loadTestKubeOpts)
		namespacePatch := tests.NewJSONPatchBuilder().
			Add("/metadata/name", consts.LoadTestVMNamespace).
			MustBuild()
		install.InstallVMCluster(ctx, t, loadTestKubeOpts, consts.LoadTestVMNamespace, vmClient, []jsonpatch.Patch{namespacePatch})

		// Point VMAgent at the load-test VMCluster.
		defaultVMClient := install.GetVMClient(t, defaultKubeOpts)
		remoteWriteURL := fmt.Sprintf(
			"http://%s/insert/0/prometheus/api/v1/write",
			consts.GetVMInsertSvc(consts.LoadTestVMNamespace, consts.LoadTestVMNamespace))
		logger.Default.Logf(t, "Setting vmagent remote write URL to %s", remoteWriteURL)
		install.EnsureVMAgentRemoteWriteURL(ctx, t, defaultVMClient, defaultKubeOpts, consts.DefaultVMNamespace, consts.DefaultReleaseName, remoteWriteURL)

		// Add custom alert rules
		install.AddCustomAlertRules(ctx, t, consts.DefaultVMNamespace)

		// Prepare namespace for k6 tests
		k6KubeOpts := k8s.NewKubectlOptions("", "", consts.K6TestsNamespace)
		k8s.CreateNamespace(t, k6KubeOpts, consts.K6TestsNamespace)
	})

	AfterEach(func() {
		defer func() {
			kubeOpts := k8s.NewKubectlOptions("", "", consts.K6TestsNamespace)
			k8s.DeleteNamespace(t, kubeOpts, consts.K6TestsNamespace)
		}()

		loadTestKubeOpts := k8s.NewKubectlOptions("", "", consts.LoadTestVMNamespace)
		gather.K8sAfterAll(ctx, t, loadTestKubeOpts, consts.ResourceWaitTimeout)
		gather.VMAfterAll(ctx, t, consts.ResourceWaitTimeout, consts.LoadTestVMNamespace)

		defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		gather.K8sAfterAll(ctx, t, defaultKubeOpts, consts.ResourceWaitTimeout)
	})

	Describe("Inner", func() {
		kubeOpts := k8s.NewKubectlOptions("", "", consts.LoadTestVMNamespace)
		vmClient := install.GetVMClient(t, kubeOpts)

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

		DescribeTable("prw2-50vus-10mins load test",
			func(backgroundFn backgroundFunc) {
				scenario := "prw2-50vus-10mins"
				parallelism := 3

				err := install.RunK6Scenario(ctx, t, consts.K6TestsNamespace, consts.LoadTestVMNamespace, consts.LoadTestVMNamespace, scenario, parallelism)
				require.NoError(t, err)

				cycleCtx, cancelCycle := context.WithCancel(ctx)
				defer cancelCycle()
				var wg sync.WaitGroup
				if backgroundFn != nil {
					wg.Add(1)
					go func() {
						defer GinkgoRecover()
						defer wg.Done()
						backgroundFn(cycleCtx)
					}()
				}

				By("Waiting for K6 jobs to complete")
				install.WaitForK6JobsToComplete(ctx, t, consts.K6TestsNamespace, scenario, parallelism)
				cancelCycle()
				wg.Wait()

				By("PRW v2 rows were inserted without errors")
				_, value, err := overwatch.VectorScan(ctx, "sum(vm_rows_inserted_total)")
				require.NoError(t, err)
				require.Greater(t, value, float64(200_000))

				By("No rows were ignored")
				_, value, err = overwatch.VectorScan(ctx, "sum(vm_rows_ignored_total)")
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(0), value)

				_, value, err = overwatch.VectorScan(ctx, "sum(vm_rows_invalid_total)")
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(0), value)

				By("k6 insert and read requests were made")
				_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_reqs_total{scenario="insert"})`)
				require.NoError(t, err)
				require.Greater(t, value, float64(1_300_000))

				_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_reqs_total{scenario="read"})`)
				require.NoError(t, err)
				require.Greater(t, value, float64(10_000))

				By("No k6 requests failed")
				_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_req_failed_rate{scenario="insert"})`)
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(0), value)

				_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_req_failed_rate{scenario="read"})`)
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(0), value)

				_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_req_duration_p95{scenario="insert"})`)
				require.NoError(t, err)
				require.Less(t, value, float64(0.5))

				_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_req_duration_p95{scenario="read"})`)
				require.NoError(t, err)
				require.Less(t, value, float64(10))
			},
			Entry("baseline", Label("id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"), backgroundFunc(nil)),
			Entry("with VMInsert replica cycling", Label("id=6bbeb19c-85bb-45df-8f1f-d95068bec025"), backgroundFunc(func(cycleCtx context.Context) {
				install.WaitForVMClusterToBeOperational(cycleCtx, t, kubeOpts, consts.LoadTestVMNamespace, vmClient)
				for cycleCtx.Err() == nil {
					install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, consts.LoadTestVMNamespace, consts.LoadTestVMNamespace, vmClient, scaleInsertReplicas(3))
					install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, consts.LoadTestVMNamespace, consts.LoadTestVMNamespace, vmClient, scaleInsertReplicas(2))
				}
			})),
			Entry("with VMStorage replica cycling", Label("id=b2c3d4e5-f6a7-8901-bcde-f12345678901"), backgroundFunc(func(cycleCtx context.Context) {
				install.WaitForVMClusterToBeOperational(cycleCtx, t, kubeOpts, consts.LoadTestVMNamespace, vmClient)
				for cycleCtx.Err() == nil {
					install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, consts.LoadTestVMNamespace, consts.LoadTestVMNamespace, vmClient, scaleStorageReplicas(3))
					install.UpdateVMClusterSpec(cycleCtx, t, kubeOpts, consts.LoadTestVMNamespace, consts.LoadTestVMNamespace, vmClient, scaleStorageReplicas(2))
				}
			})),
		)
	})
})
