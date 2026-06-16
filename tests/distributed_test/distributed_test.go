package distributed_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

		// Stage 1 (parallel): discover ingress host + install k6 + install chaos mesh.
		var wg sync.WaitGroup
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
			defer wg.Done()
			chaosCfg := tests.DefaultChaosMeshConfig()
			install.InstallChaosMesh(ctx, chaosCfg.HelmChart, chaosCfg.ValuesFile, t, chaosCfg.Namespace, chaosCfg.ReleaseName)
		}()
		wg.Wait()

		// Stage 2 (parallel): install vmgather + vm k8s stack (both need nginx host).
		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMGather(ctx, t)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMK8StackWithHelm(ctx, consts.VMK8sStackChart, consts.SmokeValuesFile(), t, consts.DefaultVMNamespace, consts.DefaultReleaseName)
			install.InstallVictoriaLogs(ctx, t, consts.DefaultVMNamespace, consts.DefaultVLReleaseName, consts.DefaultVLCollectorReleaseName)
		}()
		wg.Wait()

		// Stage 3 (parallel): overwatch + delete stock vmcluster.
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.DeleteVMCluster(t, kubeOpts, consts.DefaultReleaseName)
		}()
		wg.Wait()
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
		tests.GatherOnFailure(ctx, t, kubeOpts, namespace)

		k8s.RunKubectlContext(t, ctx, kubeOpts, "delete", "vmdistributed", "--all", "--ignore-not-found=true")
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should support reading and writing over global and local endpoints", Label("id=b81bf219-e97c-49fc-8050-8d80153224c7"), func(ctx context.Context) {
		By(fmt.Sprintf("Installing VMDistributed in namespace %s", namespace))
		install.InstallVMDistributed(ctx, t, namespace, consts.DefaultReleaseName)

		vmAuthWriteURL := install.VMDistributedRemoteWriteURL(namespace)
		vmAuthSelectURL := fmt.Sprintf("http://%s/select/0/prometheus", consts.VMAuthHost(namespace))

		// Build remote write helper for global endpoint
		globalWriter := tests.NewRemoteWriteBuilder().
			WithHTTPClient(c).
			WithURL(vmAuthWriteURL)

		By("Insert data into global write endpoint")
		fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
			WithCount(10).
			WithValue(1).
			Build()
		err := globalWriter.Send(fooTimeSeries)
		require.NoError(t, err)

		tests.WaitForDataPropagation()

		By("Read data from global read endpoint")
		globalProm := tests.NewPromClientBuilder().
			WithBaseURL(vmAuthSelectURL).
			WithStartTime(overwatch.Start).
			MustBuild()

		_, value, err := tests.RetryVectorScan(ctx, t, namespace, globalProm, "foo_2", 5)
		require.NoError(t, err)
		tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(1))
	})

	// DistributedLoadScenario holds configuration for a single VMDistributed load test variant.
	type DistributedLoadScenario struct {
		ScenarioName     string
		SetupFunc        func(ctx context.Context, namespace string)
		VerificationFunc func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string)
	}

	firstDistributedZone := func() string {
		zones := strings.Split(consts.DistributedZones(), ",")
		return strings.TrimSpace(zones[0])
	}

	oneZoneDisruptionSetupFunc := func(ctx context.Context, namespace string) {
		zone := firstDistributedZone()
		By(fmt.Sprintf("Disrupting distributed zone %s for 3 minutes", zone))
		install.ApplyVMDistributedZoneDisruptionChaos(ctx, t, namespace, zone, 3*time.Minute)
	}

	runDistributedLoadScenario := func(ctx context.Context, scenario DistributedLoadScenario) {
		overwatch, err := tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		By(fmt.Sprintf("Installing VMDistributed in namespace %s", namespace))
		install.InstallVMDistributed(ctx, t, namespace, consts.DefaultReleaseName)

		// Route writes through the global VMAuth ingress created by VMDistributed using
		// protobuf remote write (same as load tests).
		vmauthWriteURL := install.VMDistributedRemoteWriteURL(namespace)
		// Route reads through the global VMAuth ingress created by VMDistributed.
		// VMAuth accepts /select/.+ and load-balances reads across all availability zones.
		vmauthReadURL := fmt.Sprintf("http://%s/select/0/prometheus/api/v1/query_range", consts.VMAuthHost(namespace))

		const k6Scenario = "distributed-50vus-10mins"
		const parallelism = 3

		extraEnvVars := map[string]string{
			"VMINSERT_URL": vmauthWriteURL,
			"VMSELECT_URL": vmauthReadURL,
			"VM_NAMESPACE": namespace,
		}
		err = install.RunK6Scenario(ctx, t, namespace, consts.DefaultReleaseName, k6Scenario, parallelism, scenario.ScenarioName, extraEnvVars)
		require.NoError(t, err)
		if scenario.SetupFunc != nil {
			scenario.SetupFunc(ctx, namespace)
		}

		By("Waiting for K6 jobs to complete")
		install.WaitForK6JobsToComplete(ctx, t, namespace, scenario.ScenarioName, parallelism, 15*time.Minute)

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
			`sum(vm_rows_ignored_total)`,
		).EqualTo(model.SampleValue(0))
		checkMetric(
			"No rows were invalid",
			`sum(vm_rows_invalid_total)`,
		).EqualTo(model.SampleValue(0))

		scenario.VerificationFunc(checkMetric, namespace, scenario.ScenarioName)
	}

	DescribeTable("distributed-50vus-10mins load test",
		runDistributedLoadScenario,
		Entry("baseline", Label("id=2a3b4c5d-6e7f-8901-abcd-ef2345678901"), DistributedLoadScenario{
			ScenarioName: "distributed-baseline",
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"rows were inserted without errors",
					`max_over_time(sum(vm_rows_inserted_total)[15m])`,
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
				).Less(15)
				checkMetric(
					"k6 read requests duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(20)
			},
		}),
		Entry("with one zone disruption", Label("id=3b4c5d6e-7f89-0123-bcde-f34567890123"), DistributedLoadScenario{
			ScenarioName: "distributed-zone-disruption",
			SetupFunc:    oneZoneDisruptionSetupFunc,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"rows were inserted during zone disruption",
					`max_over_time(sum(vm_rows_inserted_total)[15m])`,
				).Greater(40_000)
				checkMetric(
					"k6 insert requests were made during zone disruption",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", job_name=~"^%s.*$"})[15m])`, scenarioName),
				).Greater(40_000)
				checkMetric(
					"k6 read requests were made during zone disruption",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", job_name=~"%s.*"})[15m])`, scenarioName),
				).Greater(12_000)
				checkMetric(
					"k6 insert requests failure rate is acceptable during zone disruption",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable during zone disruption",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", job_name=~"%s.*"}[15m])) or 0`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests duration is acceptable during zone disruption",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(15)
				checkMetric(
					"k6 read requests duration is acceptable during zone disruption",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", job_name=~"%s.*"}[15m]))`, scenarioName),
				).Less(20)
			},
		}),
	)
})
