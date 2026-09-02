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

func TestVLLoadTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "VL Load test Suite", suiteConfig, reporterConfig)
}

var (
	t terratesting.TestingT
)

func waitForK6MetricsScraped(ctx context.Context, t terratesting.TestingT, overwatch promquery.PrometheusClient, scenarioName string, start time.Time) time.Time {
	var metricEnd time.Time
	require.Eventually(t, func() bool {
		metricEnd = time.Now()
		values, _, err := overwatch.QueryRangeAt(ctx, fmt.Sprintf(`sum(k6_http_reqs_total{testrun_name=~"^%s.*$"})`, scenarioName), start, metricEnd)
		if err != nil {
			return false
		}
		matrix, ok := values.(model.Matrix)
		if !ok {
			return false
		}
		for _, stream := range matrix {
			if len(stream.Values) > 0 {
				return true
			}
		}
		return false
	}, consts.PollingTimeout, consts.PollingInterval, "k6 metrics for %s were not scraped", scenarioName)
	return metricEnd
}

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

		// Stage 1: install VPA + Gateway API CRDs before the operator starts. Doing this
		// first (not after InstallVMK8StackWithHelm) means the operator's own RESTMapper
		// discovers these Kinds at boot instead of racing a CRD applied after it is already
		// running - that race made the operator hard-fail reconciles with
		// `no matches for kind "VerticalPodAutoscaler"` until its cache eventually refreshed.
		defaultKubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.EnsureVPACRDs(ctx, t, defaultKubeOpts)
		install.EnsureGatewayAPICRDs(ctx, t, defaultKubeOpts)

		// Stage 2: discover ingress host + install k6 + install chaos-mesh (parallel).
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
			defer GinkgoRecover()
			defer wg.Done()
			cfg := tests.DefaultChaosMeshConfig()
			install.InstallChaosMesh(ctx, cfg.HelmChart, cfg.ValuesFile, t, cfg.Namespace, cfg.ReleaseName)
		}()
		wg.Wait()

		// Stage 3: install VM k8s stack (overwatch needs this for metrics storage).
		install.InstallVMK8StackWithHelm(
			ctx,
			consts.VMK8sStackChart(),
			consts.SmokeValuesFile(),
			t,
			consts.DefaultVMNamespace,
			consts.DefaultReleaseName,
		)

		// Stage 4: install overwatch + vmgather, and delete stale namespaces.
		tests.CleanupStaleNamespaces(ctx, t, defaultKubeOpts, "vl-load-test=true")

		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMGather(ctx, t)
		}()
		wg.Wait()
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = Describe("VL Load tests", Label("vl-load-test"), func() {

	// LoadScenario holds configuration for a single VL load test run.
	type LoadScenario struct {
		ScenarioName string
		// K6Scenario is the base name of the k6 JavaScript file under manifests/load-tests/
		// (without the .js extension). Defaults to "vl-insert-query-10mins" when empty.
		K6Scenario string
		// Patches are JSON patches applied to the VLCluster manifest before installation.
		Patches []jsonpatch.Patch
		// PreInstallFunc, if non-nil, is called after the namespace is created but before
		// VLCluster installation. Returns additional patches to apply to the VLCluster manifest.
		PreInstallFunc func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) []jsonpatch.Patch
		// SetupFunc, if non-nil, is called after VLCluster is installed and before the k6 run.
		// Use it to start background chaos scenarios or other post-install operations.
		SetupFunc func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace, clusterName string)
		// ExtraEnvVarsFunc, if non-nil, returns extra env vars for the k6 runner.
		ExtraEnvVarsFunc func(namespace string) map[string]string
		// K6MaxDuration overrides the default k6 wait timeout. Zero means use 20m.
		K6MaxDuration time.Duration
		// SkipCommonChecks disables the shared VL health assertions (row drops, HTTP errors,
		// remote send errors). Set this for scenarios where degraded behavior is expected
		// by design (e.g. near-full-storage where rows are intentionally dropped).
		SkipCommonChecks bool
		VerificationFunc func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string)
	}

	runLoadScenario := func(ctx context.Context, scenario LoadScenario) {
		testStart := time.Now()
		overwatch, err := tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		scenarioName := scenario.ScenarioName
		namespace := tests.RandomNamespace(fmt.Sprintf("vl-load-%s", scenarioName))
		clusterName := tests.ClusterName(fmt.Sprintf("vl-load-%s", scenarioName))

		kubeOpts := k8s.NewKubectlOptions("", "", namespace)

		DeferCleanup(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)

			install.DeleteVLCluster(t, kubeOpts, clusterName)
			if scenario.PreInstallFunc != nil {
				install.DeleteNFSResources(ctx, t, namespace)
			}
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		tests.CleanupNamespace(t, kubeOpts, namespace)
		tests.EnsureNamespaceExists(t, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "vl-load-test=true", "--overwrite")

		vlClient := install.GetVMClient(t, kubeOpts)

		affinity := tests.VLClusterAffinity(clusterName, "vl-load-test")

		patches := scenario.Patches
		if scenario.PreInstallFunc != nil {
			extraPatches := scenario.PreInstallFunc(ctx, kubeOpts, namespace)
			patches = append(patches, extraPatches...)
		}
		for _, component := range []string{"vlinsert", "vlselect", "vlstorage"} {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/metadata/name", clusterName).
				Add(fmt.Sprintf("/spec/%s/affinity", component), affinity).
				MustBuild())
		}

		// Resource allocation for VL components on monitoring nodes.
		type componentResources struct{ cpuReq, memReq, memLimit string }
		componentResourceMap := map[string]componentResources{
			"vlinsert":  {"300m", "256Mi", "512Mi"},
			"vlselect":  {"300m", "512Mi", "1Gi"},
			"vlstorage": {"400m", "1Gi", "2Gi"},
		}
		for component, res := range componentResourceMap {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add(fmt.Sprintf("/spec/%s/resources/requests/cpu", component), res.cpuReq).
				Add(fmt.Sprintf("/spec/%s/resources/requests/memory", component), res.memReq).
				Add(fmt.Sprintf("/spec/%s/resources/limits/memory", component), res.memLimit).
				MustBuild())
		}

		install.InstallVLCluster(ctx, t, kubeOpts, namespace, vlClient, patches, consts.PollingTimeout)
		By("VLCluster is available")

		if scenario.SetupFunc != nil {
			scenario.SetupFunc(ctx, kubeOpts, namespace, clusterName)
		}

		k6Scenario := scenario.K6Scenario
		if k6Scenario == "" {
			k6Scenario = "vl-insert-query-10mins"
		}
		const parallelism = 3

		var extraEnvVars map[string]string
		if scenario.ExtraEnvVarsFunc != nil {
			extraEnvVars = scenario.ExtraEnvVarsFunc(namespace)
		}
		metricStart := time.Now()
		err = install.RunK6VLScenario(ctx, t, namespace, clusterName, k6Scenario, parallelism, scenarioName, extraEnvVars)
		require.NoError(t, err)

		By("Waiting for K6 jobs to complete")
		k6WaitDuration := 20 * time.Minute
		if scenario.K6MaxDuration > 0 {
			k6WaitDuration = scenario.K6MaxDuration
		}
		install.WaitForK6JobsToComplete(ctx, t, namespace, scenarioName, parallelism, k6WaitDuration)
		tests.WaitForDataPropagation()
		metricEnd := waitForK6MetricsScraped(ctx, t, overwatch, scenarioName, metricStart)

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

		if !scenario.SkipCommonChecks {
			checkMetric(
				"k6 read workload did not drop scheduled iterations",
				fmt.Sprintf(`sum(max_over_time(k6_dropped_iterations_total{scenario="read", testrun_name=~"^%s.*$"}[30m])) or vector(0)`, scenarioName),
			).EqualTo(model.SampleValue(0))

			// VL-native ingestion health: rows were accepted and none were dropped due to
			// oversized timestamps, too-many-fields, or storage errors.
			checkMetric(
				"VL rows were ingested",
				fmt.Sprintf(`max_over_time(sum(vl_rows_ingested_total{namespace="%s"})[30m:])`, namespace),
			).Greater(100)
			checkMetric(
				"No VL rows were dropped",
				fmt.Sprintf(`max_over_time(sum(vl_rows_dropped_total{namespace="%s", reason!="debug"})[30m:]) or vector(0)`, namespace),
			).EqualTo(model.SampleValue(0))

			// HTTP-level errors on the VL insert and select paths must be zero.
			checkMetric(
				"No VL HTTP insert errors",
				fmt.Sprintf(`max_over_time(sum(vl_http_errors_total{namespace="%s", path=~"/insert.*"})[30m:]) or vector(0)`, namespace),
			).EqualTo(model.SampleValue(0))
			checkMetric(
				"No VL HTTP select errors",
				fmt.Sprintf(`max_over_time(sum(vl_http_errors_total{namespace="%s", path=~"/select.*"})[30m:]) or vector(0)`, namespace),
			).EqualTo(model.SampleValue(0))

			// Concurrency overload: no queries should be dropped due to queue timeout.
			checkMetric(
				"No VL concurrent select timeouts",
				fmt.Sprintf(`max_over_time(sum(vl_concurrent_select_limit_timeout_total{namespace="%s"})[30m:]) or vector(0)`, namespace),
			).EqualTo(model.SampleValue(0))

			// Replica loss during cycling causes expected transient send errors.
			if scenario.ScenarioName != "vlstorage-cycling" {
				checkMetric(
					"No VL cluster remote send errors",
					fmt.Sprintf(`max_over_time(sum(vl_insert_remote_send_errors_total{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
			}
		}

		scenario.VerificationFunc(checkMetric, namespace, scenarioName)
	}

	DescribeTable("vl-insert-query-10mins load test",
		runLoadScenario,
		// Baseline: steady log ingest (500 lines/s, 20 read VUs) against a 2-replica
		// VLCluster for 10 minutes. No chaos. Establishes the performance floor:
		// log insertion throughput, k6 request counts, failure rates, and p95 latency.
		Entry("baseline", Label("id=b1c2d3e4-f5a6-7890-bcde-f01234567890"), SpecTimeout(25*time.Minute), LoadScenario{
			ScenarioName: "baseline",
			ExtraEnvVarsFunc: func(_ string) map[string]string {
				return map[string]string{"SCENARIO_DURATION": "10m"}
			},
			K6MaxDuration: 15 * time.Minute,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"k6 insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(1_000)
				checkMetric(
					"k6 read requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="read", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(500)
				checkMetric(
					"k6 insert requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 read requests failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="read", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(10)
				checkMetric(
					"k6 insert requests p95 duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="insert", testrun_name=~"%s.*"}[30m]))`, scenarioName),
				).Less(5)
				checkMetric(
					"k6 read requests p95 duration is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario="read", testrun_name=~"%s.*"}[30m]))`, scenarioName),
				).Less(30)
				// VL storage must not flip to read-only during baseline run.
				checkMetric(
					"VL storage is not read-only",
					fmt.Sprintf(`max_over_time(max(vl_storage_is_read_only{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
				// Bytes were actually written to storage.
				checkMetric(
					"VL bytes were ingested",
					fmt.Sprintf(`max_over_time(sum(vl_bytes_ingested_total{namespace="%s"})[30m:])`, namespace),
				).Greater(1_000)
				// Streams were created (at least one unique stream field combination).
				checkMetric(
					"VL log streams were created",
					fmt.Sprintf(`max_over_time(sum(vl_streams_created_total{namespace="%s"})[30m:])`, namespace),
				).Greater(0)
				// Note: vl_concurrent_select_limit_reached_total counts queued (not failed)
				// requests; it is expected to be non-zero under any read load. The hard failure
				// signal is vl_concurrent_select_limit_timeout_total which is checked in
				// the common section above.
			},
		}),
		// High-throughput: 5x default insert rate to stress vlinsert and vlstorage
		// ingestion pipeline. Checks that throughput scales and failure rate stays low.
		Entry("high-throughput", Label("id=d3e4f5a6-b7c8-9012-defa-123456789012"), SpecTimeout(25*time.Minute), LoadScenario{
			ScenarioName: "high-throughput",
			ExtraEnvVarsFunc: func(_ string) map[string]string {
				return map[string]string{
					"SCENARIO_DURATION":   "10m",
					"K6_INSERT_RATE":      "2250",
					"K6_READ_VUS":         "45",
					"K6_BATCH_SIZE":       "20",
					"K6_PREALLOCATED_VUS": "150",
					"K6_MAX_VUS":          "500",
				}
			},
			K6MaxDuration: 15 * time.Minute,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"k6 insert requests were made at high rate",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(5_000)
				checkMetric(
					"k6 insert failure rate is acceptable under high load",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(20)
				// Large ingestion volume should be reflected in bytes counter.
				checkMetric(
					"VL ingested significant byte volume under high throughput",
					fmt.Sprintf(`max_over_time(sum(vl_bytes_ingested_total{namespace="%s"})[30m:])`, namespace),
				).Greater(10_000)
				// Insert flush duration p99 should stay below 5s even under heavy load.
				checkMetric(
					"VL insert flush p99 duration is acceptable under high load",
					fmt.Sprintf(`max(max_over_time(vl_insert_flush_duration_seconds{namespace="%s", quantile="0.99"}[30m]))`, namespace),
				).Less(5)
				// Storage must not flip to read-only (PVC is default 5Gi — sufficient).
				checkMetric(
					"VL storage is not read-only under high throughput",
					fmt.Sprintf(`max_over_time(max(vl_storage_is_read_only{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
			},
		}),
		// vlstorage pod cycling: chaos-mesh kills vlstorage-0, waits 90s, then kills
		// vlstorage-1. Validates that vlinsert retries against remaining replicas and
		// that no rows are permanently lost during pod churn.
		Entry("vlstorage pod cycling", Label("id=e4f5a6b7-c8d9-0123-efab-345678901234"), SpecTimeout(30*time.Minute), LoadScenario{
			ScenarioName: "vlstorage-cycling",
			ExtraEnvVarsFunc: func(_ string) map[string]string {
				return map[string]string{"SCENARIO_DURATION": "10m"}
			},
			K6MaxDuration: 20 * time.Minute,
			SetupFunc: func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace, _ string) {
				install.ApplyChaosScenario(ctx, t, namespace, "pods", "vlstorage-pod-restart-cycling")
			},
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"k6 insert requests were made despite pod cycling",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(1_000)
				checkMetric(
					"k6 insert failure rate is acceptable during vlstorage cycling",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(20)
				// Rows ingested should still be significant — vlinsert retries absorb transient failures.
				checkMetric(
					"VL rows were ingested despite pod cycling",
					fmt.Sprintf(`max_over_time(sum(vl_rows_ingested_total{namespace="%s"})[30m:])`, namespace),
				).Greater(1_000)
				checkMetric(
					"VL storage is not permanently read-only after cycling",
					fmt.Sprintf(`max_over_time(max(vl_storage_is_read_only{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
			},
		}),
		// Loki push protocol: ingests logs via POST /insert/loki/api/v1/push (JSON body
		// with Loki-style stream + values). Validates that the Loki-compatible endpoint
		// handles the same steady load as the baseline without errors.
		Entry("loki push protocol", Label("id=f5a6b7c8-d9e0-1234-fabc-456789012345"), SpecTimeout(25*time.Minute), LoadScenario{
			ScenarioName: "loki",
			K6Scenario:   "vl-loki-push-10mins",
			ExtraEnvVarsFunc: func(_ string) map[string]string {
				return map[string]string{
					"SCENARIO_DURATION": "10m",
					"K6_INSERT_RATE":    "1500",
					"K6_READ_VUS":       "30",
					"K6_BATCH_SIZE":     "20",
				}
			},
			K6MaxDuration: 15 * time.Minute,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"k6 loki insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(1_000)
				checkMetric(
					"k6 loki insert failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(10)
				checkMetric(
					"VL bytes were ingested via Loki push",
					fmt.Sprintf(`max_over_time(sum(vl_bytes_ingested_total{namespace="%s"})[30m:])`, namespace),
				).Greater(1_000)
				checkMetric(
					"VL log streams were created via Loki push",
					fmt.Sprintf(`max_over_time(sum(vl_streams_created_total{namespace="%s"})[30m:])`, namespace),
				).Greater(0)
				checkMetric(
					"VL storage is not read-only during loki push",
					fmt.Sprintf(`max_over_time(max(vl_storage_is_read_only{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
			},
		}),
		// High-cardinality streams: generates logs with many bounded-random combinations of
		// stream_id, service, and level as stream fields. This stresses vlstorage
		// stream indexing and validates that cardinality explosion is handled without OOM.
		Entry("high-cardinality streams", Label("id=a6b7c8d9-e0f1-2345-abcd-567890123456"), SpecTimeout(25*time.Minute), LoadScenario{
			ScenarioName: "high-cardinality",
			ExtraEnvVarsFunc: func(_ string) map[string]string {
				// Include bounded-random stream_id to create many unique streams.
				return map[string]string{
					"SCENARIO_DURATION":   "10m",
					"K6_INSERT_RATE":      "500",
					"K6_BATCH_SIZE":       "20",
					"VL_STREAM_FIELDS":    "namespace,service,level,stream_id",
					"K6_PREALLOCATED_VUS": "100",
					"K6_MAX_VUS":          "200",
				}
			},
			K6MaxDuration: 15 * time.Minute,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"k6 insert requests were made under high cardinality",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(500)
				checkMetric(
					"k6 insert failure rate is acceptable under high cardinality",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(10)
				// Many streams must have been created.
				checkMetric(
					"VL created many log streams under high cardinality",
					fmt.Sprintf(`max_over_time(sum(vl_streams_created_total{namespace="%s"})[30m:])`, namespace),
				).Greater(10)
				checkMetric(
					"VL storage is not read-only under high cardinality",
					fmt.Sprintf(`max_over_time(max(vl_storage_is_read_only{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
			},
		}),
		// NFS storage: vlstorage PVC is bound to an NFS-backed StorageClass to validate
		// that log ingestion works correctly on network-attached storage (e.g. for cloud NFS).
		Entry("with NFS storage", Label("id=b7c8d9e0-f1a2-3456-bcde-678901234567"), SpecTimeout(25*time.Minute), LoadScenario{
			ScenarioName: "nfs-storage",
			ExtraEnvVarsFunc: func(_ string) map[string]string {
				return map[string]string{
					"SCENARIO_DURATION":   "10m",
					"K6_INSERT_RATE":      "1500",
					"K6_READ_VUS":         "30",
					"K6_BATCH_SIZE":       "20",
					"K6_PREALLOCATED_VUS": "100",
					"K6_MAX_VUS":          "300",
				}
			},
			K6MaxDuration: 15 * time.Minute,
			PreInstallFunc: func(ctx context.Context, kubeOpts *k8s.KubectlOptions, namespace string) []jsonpatch.Patch {
				scName := install.InstallNFSServer(ctx, t, kubeOpts, namespace)
				return []jsonpatch.Patch{
					tests.NewJSONPatchBuilder().
						Add("/spec/vlstorage/storage/volumeClaimTemplate/spec/storageClassName", scName).
						MustBuild(),
				}
			},
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"k6 insert requests were made on NFS storage",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(1_000)
				checkMetric(
					"k6 insert failure rate is acceptable on NFS storage",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(10)
				checkMetric(
					"VL bytes were ingested on NFS storage",
					fmt.Sprintf(`max_over_time(sum(vl_bytes_ingested_total{namespace="%s"})[30m:])`, namespace),
				).Greater(1_000)
				checkMetric(
					"VL storage is not read-only on NFS",
					fmt.Sprintf(`max_over_time(max(vl_storage_is_read_only{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
			},
		}),
		// Elasticsearch bulk ingestion: sends logs via POST /insert/elasticsearch/_bulk (NDJSON).
		// Validates that the Elasticsearch-compatible endpoint accepts logs and stores them correctly.
		Entry("with Elasticsearch bulk ingestion", Label("id=c8d9e0f1-a2b3-4567-cdef-789012345678"), SpecTimeout(25*time.Minute), LoadScenario{
			ScenarioName: "es-bulk",
			K6Scenario:   "vl-es-push-10mins",
			ExtraEnvVarsFunc: func(_ string) map[string]string {
				return map[string]string{
					"SCENARIO_DURATION": "10m",
					"K6_INSERT_RATE":    "1000",
					"K6_READ_VUS":       "30",
					"K6_BATCH_SIZE":     "20",
				}
			},
			K6MaxDuration: 15 * time.Minute,
			VerificationFunc: func(checkMetric func(purpose, query string) tests.ScannedMetric, namespace, scenarioName string) {
				checkMetric(
					"k6 Elasticsearch bulk insert requests were made",
					fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario="insert", testrun_name=~"^%s.*$"})[30m:])`, scenarioName),
				).Greater(500)
				checkMetric(
					"k6 Elasticsearch bulk insert failure rate is acceptable",
					fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario="insert", testrun_name=~"%s.*"}[30m])) or vector(0)`, scenarioName),
				).Less(10)
				checkMetric(
					"VL bytes were ingested via Elasticsearch bulk",
					fmt.Sprintf(`max_over_time(sum(vl_bytes_ingested_total{namespace="%s"})[30m:])`, namespace),
				).Greater(500)
				checkMetric(
					"VL storage is not read-only during Elasticsearch bulk ingestion",
					fmt.Sprintf(`max_over_time(max(vl_storage_is_read_only{namespace="%s"})[30m:]) or vector(0)`, namespace),
				).EqualTo(model.SampleValue(0))
			},
		}),
	)
})
