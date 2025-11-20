package distributed_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestDistributedChartTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "DistributedChart test Suite", suiteConfig, reporterConfig)
}

var (
	t         terratesting.TestingT
	namespace string
	overwatch promquery.PrometheusClient
	c         *http.Client
)

// Install VM from helm chart for the first process, set namespace for the rest
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()
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

		// Remove stock VMCluster - it would be recreated in vm* namespaces
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.DeleteVMCluster(t, kubeOpts, consts.DefaultReleaseName)

		// Prepare namespace for k6 tests
		kubeOpts = k8s.NewKubectlOptions("", "", consts.K6TestsNamespace)
		if _, err := k8s.GetNamespaceE(t, kubeOpts, consts.K6OperatorNamespace); err != nil {
			k8s.CreateNamespace(t, kubeOpts, consts.K6TestsNamespace)
			k8s.RunKubectl(t, kubeOpts, "label", "namespace", consts.K6TestsNamespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
		}

		install.InstallK6(ctx, t, consts.K6OperatorNamespace)
	}, func(ctx context.Context) {
		t = tests.GetT()
		namespace = tests.RandomNamespace("vm")
	},
)

var _ = Describe("Distributed chart", Label("vmcluster"), func() {
	BeforeEach(func(ctx context.Context) {
		var err error
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		c = tests.NewHTTPClient()
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)

		helmOpts := &helm.Options{
			KubectlOptions: kubeOpts,
		}
		helm.Delete(t, helmOpts, consts.DefaultReleaseName, true)
		tests.CleanupNamespace(t, kubeOpts, namespace)

	})

	It("should support reading and writing over global and local endpoints", Label("id=b81bf219-e97c-49fc-8050-8d80153224c7"), func(ctx context.Context) {
		By(fmt.Sprintf("Installing distributed-chart in namespace %s", namespace))
		install.InstallVMDistributedWithHelm(
			ctx,
			consts.VMDistributedChart,
			consts.DistributedValuesFile(),
			t,
			namespace,
			consts.DefaultReleaseName,
		)

		// Build remote write helper for global endpoint
		globalWriter := tests.NewRemoteWriteBuilder().
			WithHTTPClient(c).
			WithURL(tests.GlobalInsertURL(namespace))

		By("Insert data into global write endpoint")
		fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
			WithCount(10).
			WithValue(1).
			Build()
		err := globalWriter.Send(fooTimeSeries)
		require.NoError(t, err)

		By("Read data from global read endpoint")
		globalProm := tests.NewPromClientBuilder().
			WithBaseURL(tests.GlobalSelectURL(namespace)).
			WithStartTime(overwatch.Start).
			MustBuild()

		_, value, err := tests.RetryVectorScan(ctx, t, namespace, globalProm, "foo_2", 5)
		require.NoError(t, err)
		require.Equal(t, value, model.SampleValue(1))

		for _, zone := range strings.Split(consts.DistributedZones(), ",") {
			if zone == "" {
				continue
			}
			By(fmt.Sprintf("Read data from zone %s endpoint", zone))
			zoneProm := tests.NewPromClientBuilder().
				WithBaseURL(tests.ZoneSelectURL(zone)).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, zoneProm, "foo_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))
		}
	})

	It("should handle load test", Label("id=fc171682-00dc-48ee-9686-5eea85890078"), func(ctx context.Context) {
		By(fmt.Sprintf("Installing distributed-chart in namespace %s", namespace))
		install.InstallVMDistributedWithHelm(
			ctx,
			consts.VMDistributedChart,
			consts.DistributedValuesFile(),
			t,
			namespace,
			consts.DefaultReleaseName,
		)

		globalWriteURL := tests.GlobalInsertURL(namespace)
		globalReadURL := tests.GlobalSelectURL(namespace)

		By("Install Prometheus Benchmark")
		prombenchConfig := tests.PromBenchmarkConfig{
			DisableMonitoring: true,
			TargetsCount:      "500",
			WriteURL:          globalWriteURL,
			ReadURL:           globalReadURL,
			WriteReplicaMem:   "2G",
			WriteReplicaCPU:   "1",
		}
		install.InstallPrometheusBenchmark(ctx, t, consts.BenchmarkNamespace, prombenchConfig.ToHelmValues())

		By("Run 50vus-30mins scenario")
		scenario := "vmselect-50vus-30mins"
		err := install.RunK6Scenario(ctx, t, consts.K6TestsNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName, scenario, 3, scenario)
		require.NoError(t, err)

		By("Waiting for K6 jobs to complete")
		install.WaitForK6JobsToComplete(ctx, t, consts.K6TestsNamespace, scenario, 3)

		By("At least 50m rows were inserted")
		_, value, err := overwatch.VectorScan(ctx, "sum (vm_rows_inserted_total)")
		require.NoError(t, err)
		require.GreaterOrEqual(t, value, float64(2_500_000))

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

		By("At least 4k requests were made")
		_, value, err = overwatch.VectorScan(ctx, "sum(vm_requests_total)")
		require.NoError(t, err)
		require.GreaterOrEqual(t, value, float64(4_000))
	})
})
