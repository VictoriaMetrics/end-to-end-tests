package load_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/gather"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

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

		// Ensure VMAgent remote write URL is set up
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		vmclient := install.GetVMClient(t, kubeOpts)
		remoteWriteURL := fmt.Sprintf(
			"http://vminsert-vm.%s.svc.cluster.local.:8480/insert/0/prometheus/api/v1/write",
			consts.DefaultVMNamespace)
		logger.Default.Logf(t, "Setting vmagent remote write URL to %s", remoteWriteURL)
		install.EnsureVMAgentRemoteWriteURL(ctx, t, vmclient, kubeOpts, consts.DefaultVMNamespace, consts.DefaultReleaseName, remoteWriteURL)

		// Add custom alert rules
		install.AddCustomAlertRules(ctx, t, consts.DefaultVMNamespace)

		// Prepare namespace for k6 tests
		kubeOpts = k8s.NewKubectlOptions("", "", consts.K6TestsNamespace)
		k8s.CreateNamespace(t, kubeOpts, consts.K6TestsNamespace)
	})

	AfterEach(func() {
		defer func() {
			kubeOpts := k8s.NewKubectlOptions("", "", consts.K6TestsNamespace)
			k8s.DeleteNamespace(t, kubeOpts, consts.K6TestsNamespace)
		}()

		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		gather.K8sAfterAll(ctx, t, kubeOpts, consts.ResourceWaitTimeout)
		gather.VMAfterAll(ctx, t, consts.ResourceWaitTimeout, consts.DefaultReleaseName)
	})

	Describe("Inner", func() {
		It("Default installation should handle prometheus remote write v2 insert and read", Label("id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"), func() {
			By("Run 50vus-10mins scenario")
			scenario := "prw2-50vus-10mins"
			parallelism := 3

			err := install.RunK6Scenario(ctx, t, consts.K6TestsNamespace, consts.DefaultVMNamespace, scenario, parallelism)
			require.NoError(t, err)

			By("Waiting for K6 jobs to complete")
			install.WaitForK6JobsToComplete(ctx, t, consts.K6TestsNamespace, scenario, parallelism)

			By("PRW v2 rows were inserted without errors")
			_, value, err := overwatch.VectorScan(ctx, "sum(vm_rows_inserted_total)")
			require.NoError(t, err)
			require.Greater(t, value, float64(200_000))

			By("No rows were ignored")
			_, value, err = overwatch.VectorScan(ctx, "sum(vm_rows_ignored_total)")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(0))

			_, value, err = overwatch.VectorScan(ctx, "sum(vm_rows_invalid_total)")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(0))

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
			require.Equal(t, value, model.SampleValue(0))

			_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_req_failed_rate{scenario="read"})`)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(0))

			_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_req_duration_p95{scenario="insert"})`)
			require.NoError(t, err)
			require.Less(t, value, float64(0.5))

			_, value, err = overwatch.VectorScan(ctx, `sum(k6_http_req_duration_p95{scenario="read"})`)
			require.NoError(t, err)
			require.Less(t, value, float64(10))
		})

		PIt("Default installation should handle 50vus-30mins load test scenario", Label("kind", "id=d37b1987-a9e7-4d13-87b7-f2ded679c249"), func() {
			By("Run 50vus-30mins scenario")
			scenario := "vmselect-50vus-30mins"

			err := install.RunK6Scenario(ctx, t, consts.K6TestsNamespace, consts.DefaultVMNamespace, scenario, 3)
			require.NoError(t, err)

			By("Waiting for K6 jobs to complete")
			install.WaitForK6JobsToComplete(ctx, t, consts.K6TestsNamespace, scenario, 3)

			// FIXME: TooHighCPUUsage intermittently fires
			// By("No alerts are firing")
			// overwatch.CheckNoAlertsFiring(ctx, t, consts.DefaultVMNamespace, nil)

			// lookbackWindow := time.Since(overwatch.Start)
			// overwatch.CheckAlertWasFiringSince(ctx, t, consts.K6TestsNamespace, "TooHighCPUUsage", lookbackWindow.String())

			By("At least 24k rows were inserted")
			_, value, err := overwatch.VectorScan(ctx, "sum (vm_rows_inserted_total)")
			require.NoError(t, err)
			require.GreaterOrEqual(t, value, float64(24_000_000))

			By("At least 400k merges were made")
			_, value, err = overwatch.VectorScan(ctx, "sum(vm_rows_merged_total)")
			require.NoError(t, err)
			require.GreaterOrEqual(t, value, float64(400_000))

			By("No rows were ignored")
			_, value, err = overwatch.VectorScan(ctx, "sum (vm_rows_ignored_total)")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(0))

			_, value, err = overwatch.VectorScan(ctx, "sum (vm_rows_invalid_total)")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(0))

			By("At least 100k requests were made")
			_, value, err = overwatch.VectorScan(ctx, "sum(vm_requests_total)")
			require.NoError(t, err)
			require.GreaterOrEqual(t, value, float64(10_000))
		})
	})
})
