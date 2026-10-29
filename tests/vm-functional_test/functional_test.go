package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

const (
	mtlsSecretName      = "vm-mtls"
	enterpriseNSLabel   = "vm-enterprise-test=true"
	vmInsertWritePath   = "/insert/0/prometheus/api/v1/write"
	resourceNamesFormat = "jsonpath={.items[*].metadata.name}"
)

func TestFunctionalTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.FlakeAttempts = 3
	RunSpecs(t, "Functional test Suite", suiteConfig, reporterConfig)
}

var (
	t         terratesting.TestingT
	namespace string
	overwatch promquery.PrometheusClient
	c         *http.Client
)

// pollUntilReady wraps a polled assertion with the suite-wide timeout and interval.
func pollUntilReady(ctx context.Context, poll interface{}) AsyncAssertion {
	return Eventually(ctx, poll).
		WithTimeout(consts.ResourceWaitTimeout).
		WithPolling(consts.PollingInterval)
}

func waitForGatewayAPIHTTPRouteAccess(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) {
	pollUntilReady(ctx, func() (string, error) {
		output, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts,
			"auth", "can-i", "list", "httproutes.gateway.networking.k8s.io",
			"--as=system:serviceaccount:monitoring:vmks-victoria-metrics-operator",
			"--all-namespaces")
		return strings.TrimSpace(output), err
	}).Should(Equal("yes"), "operator service account cannot list HTTPRoutes")
}

// nsOpts returns kubectl options for the current test namespace, creating it if needed.
func nsOpts() *k8s.KubectlOptions {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	tests.EnsureNamespaceExists(t, kubeOpts, namespace)
	return kubeOpts
}

// installVMAgentRW deploys a VMAgent remote-writing into the namespace's vminsert and
// exposes it as an ingress. extra is merged into the remoteWrite entry, e.g. to attach
// an inline relabel or stream-aggregation config.
func installVMAgentRW(ctx context.Context, kubeOpts *k8s.KubectlOptions, extra map[string]interface{}) {
	remoteWrite := map[string]interface{}{
		"url": fmt.Sprintf("http://%s%s", consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace), vmInsertWritePath),
	}
	maps.Copy(remoteWrite, extra)

	patch, err := install.CreateJsonPatch([]install.PatchOp{
		{Op: "add", Path: "/spec/remoteWrite", Value: []map[string]interface{}{remoteWrite}},
	})
	require.NoError(t, err)

	install.InstallVMAgent(ctx, t, kubeOpts, namespace, install.GetVMClient(t, kubeOpts), []jsonpatch.Patch{patch})
	install.ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)
}

// tenantProm queries the namespace's VMCluster as the given tenant.
func tenantProm(tenant int) promquery.PrometheusClient {
	return tests.NewPromClientBuilder().WithNamespace(namespace).WithTenant(tenant).WithStartTime(overwatch.Start).MustBuild()
}

// mtenantProm queries the namespace's VMCluster through its multitenant endpoint.
func mtenantProm() promquery.PrometheusClient {
	return tests.NewPromClientBuilder().WithNamespace(namespace).Multitenant().WithStartTime(overwatch.Start).MustBuild()
}

// singleProm queries the namespace's VMSingle.
func singleProm() promquery.PrometheusClient {
	return tests.NewPromClientBuilder().ForVMSingle(namespace).WithStartTime(overwatch.Start).MustBuild()
}

// postExpect posts body to url and asserts the response status code.
func postExpect(url, contentType string, body io.Reader, wantStatus int) {
	resp, err := c.Post(url, contentType, body)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, wantStatus, resp.StatusCode)
}

// datadogPayload builds a single-point Datadog gauge series tagged env:test,foo:bar.
func datadogPayload(metric string) []byte {
	payload, err := json.Marshal(tests.DatadogSeries{Series: []tests.DatadogMetric{{
		Metric: metric,
		Points: [][]interface{}{{time.Now().Unix(), 123}},
		Tags:   []string{"env:test", "foo:bar"},
		Host:   "test-host",
		Type:   "gauge",
	}}})
	require.NoError(t, err)
	return payload
}

// otlpGauge builds an OTLP protobuf request with one integer gauge point.
func otlpGauge(name string, value int64, attrKey, attrValue string) []byte {
	payload, err := proto.Marshal(&colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: name,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
				TimeUnixNano: uint64(time.Now().UnixNano()),
				Value:        &metricspb.NumberDataPoint_AsInt{AsInt: value},
				Attributes: []*commonpb.KeyValue{{Key: attrKey, Value: &commonpb.AnyValue{
					Value: &commonpb.AnyValue_StringValue{StringValue: attrValue},
				}}},
			}}}},
		}}}}}},
	})
	require.NoError(t, err)
	return payload
}

// labelEnterpriseNS marks the namespace so killed enterprise runs can be cleaned up later.
func labelEnterpriseNS(ctx context.Context, kubeOpts *k8s.KubectlOptions) {
	k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, enterpriseNSLabel, "--overwrite")
}

// expectResourceNamed waits for all resources of kind to be created and asserts one is named want.
func expectResourceNamed(ctx context.Context, kubeOpts *k8s.KubectlOptions, kind, want string) {
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", kind, "--all", "--for=jsonpath={.metadata.name}",
		fmt.Sprintf("--namespace=%s", namespace), fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))

	output, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "get", kind, "-n", namespace, "-o", resourceNamesFormat)
	require.NoError(t, err)
	Expect(output).To(ContainSubstring(want))
}

// Install VM from helm chart for the first process, set namespace for the rest
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()

		// Stage 1: install VPA + Gateway API CRDs before the operator starts. Doing this
		// first (not after InstallVMStackAndGather) means the operator's own RESTMapper
		// discovers these Kinds at boot instead of racing a CRD applied after it is already
		// running - that race made the operator hard-fail reconciles with
		// `no matches for kind "VerticalPodAutoscaler"` until its cache eventually refreshed.
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.EnsureVPACRDs(ctx, t, kubeOpts)
		install.EnsureGatewayAPICRDs(ctx, t, kubeOpts)

		// Enterprise specs (Kafka/mTLS/VMSingle) run in this same binary, gated by
		// Label("enterprise") + the VM_ENTERPRISE ginkgo label-filter. Only pay for
		// their extra setup (stale-namespace cleanup, Strimzi, K6) when a license is
		// configured for this run - that's the same signal the pipeline sets alongside
		// VM_ENTERPRISE=1.
		if consts.LicenseFile() != "" {
			tests.CleanupStaleNamespaces(ctx, t, kubeOpts, enterpriseNSLabel)

			// k6-operator is installed per-namespace in the Kafka Context's BeforeEach
			// instead of once here, since its TestRun controller hardcodes
			// MaxConcurrentReconciles=1 and a single shared instance becomes a
			// bottleneck when reconciled cluster-wide.
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				install.DiscoverIngressHost(ctx, t)
			}()
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				install.InstallStrimziOperator(ctx, t, consts.KafkaNamespace)
			}()
			wg.Wait()
		} else {
			install.DiscoverIngressHost(ctx, t)
		}

		// Stage 2 (parallel): install vmgather + vm k8s stack (both need ingress host).
		tests.InstallVMStackAndGather(ctx, t)

		// Stage 3: overwatch + delete stock vmcluster.
		tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{DeleteVMCluster: true})
	}, func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = Describe("VMCluster test", Label("vmcluster"), func() {
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		var err error
		namespace = tests.RandomNamespace("vm")
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		// Create new VMCluster object
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		vmclient := install.GetVMClient(t, kubeOpts)
		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{}, consts.VMClusterWaitTimeout)

		c = tests.NewHTTPClient()
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)

		install.DeleteVMCluster(t, kubeOpts, namespace)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	Describe("Multitenancy", func() {
		It("should not mix data sent to different tenants", Label("id=66618081-b150-4b48-8180-ae1f53512117"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			tenant0Writer := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForTenant(namespace, 0)

			tenant1Writer := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForTenant(namespace, 1)

			By("Inserting data into tenant 0")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				Build()
			err := tenant0Writer.Send(ctx, fooTimeSeries)
			require.NoError(t, err)

			By("Inserting data into tenant 1")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = tenant1Writer.Send(ctx, barTimeSeries)
			require.NoError(t, err)

			By("Verifying tenant 0 data is isolated")
			tenant0Prom := tenantProm(0)

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, tenant0Prom, "foo_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(1))

			_, value, err = tenant0Prom.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(0))

			By("Verifying tenant 1 data is isolated")
			tenant1Prom := tenantProm(1)

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, tenant1Prom, "bar_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(5))

			_, value, err = tenant1Prom.VectorScan(ctx, "foo_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(0))

			By("Verifying data can be retrieved via multitenant URL")
			multitenantProm := mtenantProm()

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "foo_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(1))

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "bar_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(5))
		})

		It("should accept data via multitenant URL", Label("id=16c08934-9e25-45ed-a94b-4fbbbe3170ef"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			multitenantWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForMultitenant(namespace)

			By("Inserting data into tenant 0 via multitenant endpoint")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				WithTenantLabel(0).
				Build()
			err := multitenantWriter.Send(ctx, fooTimeSeries)
			require.NoError(t, err)

			By("Inserting data into tenant 1 via multitenant endpoint")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				WithTenantLabel(1).
				Build()
			err = multitenantWriter.Send(ctx, barTimeSeries)
			require.NoError(t, err)

			By("Verifying tenant 0 data is isolated")
			tenant0Prom := tenantProm(0)

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, tenant0Prom, "foo_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(1))

			_, value, err = tenant0Prom.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(0))

			By("Verifying tenant 1 data is isolated")
			tenant1Prom := tenantProm(1)

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, tenant1Prom, "bar_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(5))

			_, value, err = tenant1Prom.VectorScan(ctx, "foo_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(0))
		})

		It("should retrieve data from different tenants via multitenant URL", Label("id=7e075898-f6c4-49d5-9d7f-8a6163759065"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			tenant0Writer := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForTenant(namespace, 0)

			tenant1Writer := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForTenant(namespace, 1)

			By("Inserting data into tenant 0")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				Build()
			err := tenant0Writer.Send(ctx, fooTimeSeries)
			require.NoError(t, err)

			By("Inserting data into tenant 1")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = tenant1Writer.Send(ctx, barTimeSeries)
			require.NoError(t, err)

			By("Verifying data can be retrieved via multitenant URL")
			multitenantProm := mtenantProm()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "foo_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(1))

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "bar_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(5))
		})
	})

	Describe("Relabeling", func() {
		It("should relabel data sent via remote write", Label("id=e72f26ba-c1b7-4671-9c7e-7cfa630c33a9"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			By("Configure VMAgent to relabel data")
			installVMAgentRW(ctx, nsOpts(), map[string]interface{}{
				"inlineUrlRelabelConfig": []map[string]interface{}{
					{"target_label": "cluster", "replacement": "dev"},
					{"action": "drop", "source_labels": []string{"__name__"}, "regex": "bar_.*"},
				},
			})

			vmagentWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMAgent(namespace)

			By("Inserting foo data via VMAgent (should be relabeled)")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				Build()
			err := vmagentWriter.Send(ctx, fooTimeSeries)
			require.NoError(t, err)

			By("Inserting bar data (should be dropped)")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = vmagentWriter.Send(ctx, barTimeSeries)
			require.NoError(t, err)

			By("foo has cluster=dev label")
			prom := tenantProm(0)

			labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "foo_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(1))
			require.Contains(t, labels, model.LabelName("cluster"))
			require.Equal(t, labels["cluster"], model.LabelValue("dev"))

			By("bar_2 was removed")
			_, value, err = prom.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(0))
		})
	})

	Describe("Streaming Aggregation", func() {
		It("should aggregate data with sum_samples output via VMAgent", Label("id=c3d4e5f6-a7b8-9012-cdef-345678901234"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			By("Configure VMAgent with streaming aggregation")
			installVMAgentRW(ctx, nsOpts(), map[string]interface{}{
				"streamAggrConfig": map[string]interface{}{
					"rules": []map[string]interface{}{{
						"match":    []string{`{__name__=~"cluster_aggr_.*"}`},
						"interval": "30s",
						"outputs":  []string{"sum_samples"},
						"without":  []string{"foo", "bar", "baz"},
					}},
				},
			})

			vmagentWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMAgent(namespace)

			By("Waiting for stream aggregation to initialize")
			tests.WaitForAggregation()

			By("Inserting multiple samples for aggregation")
			for i := 0; i < 5; i++ {
				aggrTimeSeries := tests.NewTimeSeriesBuilder("cluster_aggr_test").
					WithCount(3).
					WithValue(1).
					Build()
				err := vmagentWriter.Send(ctx, aggrTimeSeries)
				require.NoError(t, err)
				time.Sleep(consts.DataPropagationDelay)
			}

			By("Inserting non-matching metrics")
			nonAggrTimeSeries := tests.NewTimeSeriesBuilder("cluster_nonaggr").
				WithCount(3).
				WithValue(100).
				Build()
			err := vmagentWriter.Send(ctx, nonAggrTimeSeries)
			require.NoError(t, err)

			By("Waiting for aggregation interval to pass")
			tests.WaitForAggregation()

			By("Verifying aggregated metrics exist with correct naming")
			prom := tenantProm(0)

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "sum_over_time(cluster_aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "sum_over_time(cluster_aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])").EqualTo(model.SampleValue(5))

			By("Verifying non-matching metrics are written as-is")
			_, value, err = prom.VectorScan(ctx, "cluster_nonaggr_0")
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "cluster_nonaggr_0").EqualTo(model.SampleValue(100))

			By("Verifying original aggr metrics are dropped")
			_, value, err = prom.VectorScan(ctx, "cluster_aggr_test_0")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "cluster_aggr_test_0").EqualTo(model.SampleValue(0))
		})
	})

	Describe("Ingestion", func() {
		ddLabels := map[string]model.LabelValue{"env": "test", "foo": "bar", "host": "test-host"}

		Context("InfluxDB", func() {
			It("should ingest data via influxdb protocol to vmagent", Label("id=e5fba904-59b8-4440-97d5-9747dc78f959"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				By("Configure VMAgent to write to VMCluster")
				installVMAgentRW(ctx, nsOpts(), nil)

				By("Inserting data via InfluxDB protocol")
				influxURL := fmt.Sprintf("http://%s/write", consts.VMAgentNamespacedHost(namespace))
				postExpect(influxURL, "", strings.NewReader("influx_test,foo=bar value=123"), http.StatusNoContent)

				tests.VerifyIngestedMetric(ctx, t, namespace, tenantProm(0), "influx_test_value", model.SampleValue(123), map[string]model.LabelValue{"foo": "bar"})
			})

			It("should ingest data via influxdb protocol to vminsert", Label("id=11223344-5566-7788-9900-aabbccddeeff"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				nsOpts()

				By("Inserting data via InfluxDB protocol")
				influxURL := fmt.Sprintf("http://%s/insert/0/influx/write", consts.VMInsertHost(namespace))
				postExpect(influxURL, "", strings.NewReader("influx_vminsert_test,foo=bar value=123"), http.StatusNoContent)

				tests.VerifyIngestedMetric(ctx, t, namespace, tenantProm(0), "influx_vminsert_test_value", model.SampleValue(123), map[string]model.LabelValue{"foo": "bar"})
			})
		})

		Context("Datadog", func() {
			It("should ingest data via datadog protocol to vmagent", Label("id=6862ebb3-0d9f-4af1-9359-08692c8dfc5c"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				By("Configure VMAgent to write to VMCluster")
				installVMAgentRW(ctx, nsOpts(), nil)

				By("Inserting data via Datadog protocol")
				datadogURL := fmt.Sprintf("http://%s/datadog/api/v1/series", consts.VMAgentNamespacedHost(namespace))
				postExpect(datadogURL, "application/json", bytes.NewReader(datadogPayload("datadog.test.metric")), http.StatusAccepted)

				tests.VerifyIngestedMetric(ctx, t, namespace, tenantProm(0), "datadog.test.metric", model.SampleValue(123), ddLabels)
			})

			It("should ingest data via datadog protocol to vminsert", Label("id=aabbccdd-1122-3344-5566-77889900aabb"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				nsOpts()

				By("Inserting data via Datadog protocol")
				datadogURL := fmt.Sprintf("http://%s/insert/0/datadog/api/v1/series", consts.VMInsertHost(namespace))
				postExpect(datadogURL, "application/json", bytes.NewReader(datadogPayload("datadog.vminsert.test.metric")), http.StatusAccepted)

				tests.VerifyIngestedMetric(ctx, t, namespace, tenantProm(0), "datadog.vminsert.test.metric", model.SampleValue(123), ddLabels)
			})
		})

		Context("OpenTelemetry", func() {
			It("should ingest data via opentelemetry protocol to vminsert", Label("id=4e7c8581-2c93-4796-9817-219586111111"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				nsOpts()

				By("Inserting data via OpenTelemetry protocol")
				otelURL := fmt.Sprintf("http://%s/insert/0/opentelemetry/v1/metrics", consts.VMInsertHost(namespace))
				postExpect(otelURL, "application/x-protobuf", bytes.NewReader(otlpGauge("otel_test_metric", 123, "foo", "bar")), http.StatusOK)

				tests.VerifyIngestedMetric(ctx, t, namespace, tenantProm(0), "otel_test_metric", model.SampleValue(123), map[string]model.LabelValue{"foo": "bar"})
			})

			It("should ingest data via opentelemetry protocol to vmagent", Label("id=55667788-9900-aabb-ccdd-eeff11223344"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				By("Configure VMAgent to write to VMCluster")
				installVMAgentRW(ctx, nsOpts(), nil)

				By("Inserting data via OpenTelemetry protocol")
				otelURL := fmt.Sprintf("http://%s/opentelemetry/v1/metrics", consts.VMAgentNamespacedHost(namespace))
				postExpect(otelURL, "application/x-protobuf", bytes.NewReader(otlpGauge("otel_vmagent_test_metric", 456, "foo", "baz")), http.StatusOK)

				tests.VerifyIngestedMetric(ctx, t, namespace, tenantProm(0), "otel_vmagent_test_metric", model.SampleValue(456), map[string]model.LabelValue{"foo": "baz"})
			})
		})
	})
})

var _ = Describe("VMSingle test", Label("vmsingle"), func() {
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		var err error
		namespace = tests.RandomNamespace("vm")
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		c = tests.NewHTTPClient()
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	Describe("Relabeling", func() {
		It("should relabel data sent via remote write", Label("id=aabbccdd-eeff-0011-2233-445566778899"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			kubeOpts := nsOpts()

			By("Configure VMSingle to relabel data")
			cfgMapName := "vmsingle-relabel-config"

			relabelConfig := tests.NewRelabelConfigBuilder().
				AddLabel("cluster", "dev").
				DropByName("bar_.*").
				MustBuild()

			err := tests.NewConfigMapBuilder(cfgMapName).
				WithRelabelConfig(relabelConfig).
				Apply(ctx, t, kubeOpts)
			require.NoError(t, err)

			patch := tests.NewJSONPatchBuilder().
				WithVMSingleConfig(cfgMapName, "relabelConfig", "relabel.yml").
				MustBuild()

			vmclient := install.GetVMClient(t, kubeOpts)
			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch}, consts.ResourceWaitTimeout)

			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			By("Inserting foo data (should be relabeled)")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				Build()
			err = remoteWriter.Send(ctx, fooTimeSeries)
			require.NoError(t, err)

			By("Inserting bar data (should be dropped)")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = remoteWriter.Send(ctx, barTimeSeries)
			require.NoError(t, err)

			By("foo has cluster=dev label")
			prom := singleProm()

			labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "foo_2", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "foo_2").EqualTo(model.SampleValue(1))
			require.Contains(t, labels, model.LabelName("cluster"))
			require.Equal(t, labels["cluster"], model.LabelValue("dev"))

			By("bar_2 was removed")
			_, value, err = prom.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "bar_2").EqualTo(model.SampleValue(0))
		})
	})

	Describe("Streaming Aggregation", func() {
		It("should aggregate data with sum_samples output", Label("id=a1d80616-26b9-4007-8221-1de5aa8b1827"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			kubeOpts := nsOpts()

			By("Configure VMSingle with streaming aggregation")
			cfgMapName := "vmsingle-stream-aggr-config"

			// Build streaming aggregation config using builder
			streamAggrConfig := tests.NewStreamAggrConfigBuilder().
				AddRule(`{__name__=~"aggr_.*"}`, "30s", []string{"sum_samples"}).
				WithoutLabels("foo", "bar", "baz").
				MustBuild()

			err := tests.NewConfigMapBuilder(cfgMapName).
				WithStreamAggrConfig(streamAggrConfig).
				Apply(ctx, t, kubeOpts)
			require.NoError(t, err)

			patch := tests.NewJSONPatchBuilder().
				WithVMSingleConfig(cfgMapName, "streamAggr.config", "stream-aggr.yml").
				MustBuild()

			vmclient := install.GetVMClient(t, kubeOpts)
			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch}, consts.ResourceWaitTimeout)

			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			By("Waiting for stream aggregation to initialize")
			tests.WaitForAggregation()

			By("Inserting multiple samples for aggregation")
			for i := 0; i < 3; i++ {
				aggrTimeSeries := tests.NewTimeSeriesBuilder("aggr_test").
					WithCount(3).
					WithValue(1).
					Build()
				err = remoteWriter.Send(ctx, aggrTimeSeries)
				require.NoError(t, err)
				time.Sleep(consts.DataPropagationDelay)
			}

			By("Inserting non-matching metrics")
			nonAggrTimeSeries := tests.NewTimeSeriesBuilder("nonaggr").
				WithCount(3).
				WithValue(100).
				Build()
			err = remoteWriter.Send(ctx, nonAggrTimeSeries)
			require.NoError(t, err)

			By("Waiting for aggregation interval to pass")
			tests.WaitForAggregation()

			By("Verifying aggregated metrics exist with correct naming")
			prom := singleProm()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "sum_over_time(aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "sum_over_time(aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])").Greater(0)

			By("Verifying non-matching metrics are written as-is")
			_, value, err = prom.VectorScan(ctx, "nonaggr_0")
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "nonaggr_0").EqualTo(model.SampleValue(100))

			By("Verifying original aggr metrics are dropped")
			_, value, err = prom.VectorScan(ctx, "aggr_test_0")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			tests.NewScannedMetric(t, value, "aggr_test_0").EqualTo(model.SampleValue(0))
		})
	})

	Describe("Ingestion", func() {
		installSingle := func(ctx context.Context) {
			kubeOpts := nsOpts()
			install.InstallVMSingle(ctx, t, kubeOpts, namespace, install.GetVMClient(t, kubeOpts), nil, consts.ResourceWaitTimeout)
		}

		Context("InfluxDB", func() {
			It("should ingest data via influxdb protocol", Label("id=b2c3d4e5-f6a7-8901-ba12-345678901234"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				installSingle(ctx)

				By("Inserting data via InfluxDB protocol")
				influxURL := fmt.Sprintf("http://%s/write", consts.VMSingleNamespacedHost(namespace))
				postExpect(influxURL, "", strings.NewReader("influx_test,foo=bar value=123"), http.StatusNoContent)

				tests.VerifyIngestedMetric(ctx, t, namespace, singleProm(), "influx_test_value", model.SampleValue(123), map[string]model.LabelValue{"foo": "bar"})
			})
		})

		Context("Datadog", func() {
			It("should ingest data via datadog protocol", Label("id=905d5353-b40f-4822-a2ab-decd29f1ac12"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				installSingle(ctx)

				By("Inserting data via Datadog protocol")
				datadogURL := fmt.Sprintf("http://%s/datadog/api/v1/series", consts.VMSingleNamespacedHost(namespace))
				postExpect(datadogURL, "application/json", bytes.NewReader(datadogPayload("datadog.test.metric")), http.StatusAccepted)

				tests.VerifyIngestedMetric(ctx, t, namespace, singleProm(), "datadog.test.metric", model.SampleValue(123),
					map[string]model.LabelValue{"env": "test", "foo": "bar", "host": "test-host"})
			})
		})

		Context("OpenTelemetry", func() {
			It("should ingest data via opentelemetry protocol", Label("id=55ca0534-1111-2222-3333-444455556666"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
				installSingle(ctx)

				By("Inserting data via OpenTelemetry protocol")
				otelURL := fmt.Sprintf("http://%s/opentelemetry/v1/metrics", consts.VMSingleNamespacedHost(namespace))
				postExpect(otelURL, "application/x-protobuf", bytes.NewReader(otlpGauge("otel_test_metric", 123, "foo", "bar")), http.StatusOK)

				tests.VerifyIngestedMetric(ctx, t, namespace, singleProm(), "otel_test_metric", model.SampleValue(123), map[string]model.LabelValue{"foo": "bar"})
			})
		})
	})

	PDescribe("Backup and Restore", func() {
		It("should backup and restore data via PVC", Label("id=8576d108-7357-4555-b4fa-7e8649186c07"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
			kubeOpts := nsOpts()

			vmclient := install.GetVMClient(t, kubeOpts)

			By("Creating backup PVC")
			backupPVCName := "backup-pvc"
			install.KubectlApply(ctx, t, kubeOpts, consts.ManifestsRoot()+"/components/backup-pvc.yaml")

			By("Installing VMSingle")
			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, nil, consts.ResourceWaitTimeout)

			By("Sending data")
			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			ts := tests.NewTimeSeriesBuilder("backup_test").
				WithCount(100).
				WithValue(10).
				Build()
			err := remoteWriter.Send(ctx, ts)
			require.NoError(t, err)

			By("Verifying data before backup")
			prom := singleProm()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "backup_test_10", 5)
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "backup_test_10").EqualTo(model.SampleValue(10))

			By("Reconfiguring VMSingle with backup sidecar")
			vmBackupImage := "victoriametrics/vmbackup:latest"
			if consts.VMBackupDefaultImage() != "" {
				vmBackupImage = fmt.Sprintf("%s:%s", consts.VMBackupDefaultImage(), consts.VMBackupDefaultVersion())
			}
			backupVolumes := []map[string]interface{}{
				{
					"name":                  "backups",
					"persistentVolumeClaim": map[string]string{"claimName": backupPVCName},
				},
			}
			backupMounts := []map[string]string{
				{"name": "backups", "mountPath": "/backups"},
				{"name": "data", "mountPath": "/victoria-metrics-data"},
			}
			patch := tests.NewJSONPatchBuilder().
				Add("/spec/volumes", backupVolumes).
				Add("/spec/containers", []map[string]interface{}{
					{
						"name":         "vmbackup",
						"image":        vmBackupImage,
						"command":      []string{"tail", "-f", "/dev/null"},
						"volumeMounts": backupMounts,
					},
				}).
				MustBuild()

			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch}, consts.ResourceWaitTimeout)
			k8s.WaitUntilNumPodsCreatedContext(t, ctx, kubeOpts, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=vmsingle,app.kubernetes.io/instance=vmsingle"}, 1, consts.Retries, consts.PollingInterval)

			By("Running vmbackup in sidecar")
			cmd := []string{
				"/vmbackup-prod",
				"-dst=fs:///backups/backup1",
				"-storageDataPath=/victoria-metrics-data",
				"-snapshot.createURL=http://localhost:8429/snapshot/create",
			}
			backupContainerCmd := []string{
				"exec", "deploy/vmsingle-vmsingle", "-c", "vmbackup", "--",
				"sh", "-c", strings.Join(cmd, " "),
			}
			helpers.Logf("Executing backup command: %v", backupContainerCmd)
			k8s.RunKubectlContext(t, ctx, kubeOpts, backupContainerCmd...)

			By("Destroying VMSingle")
			install.DeleteVMSingle(t, kubeOpts, "vmsingle")
			k8s.WaitUntilNumPodsCreatedContext(t, ctx, kubeOpts, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=vmsingle,app.kubernetes.io/instance=vmsingle"}, 0, consts.Retries, consts.PollingInterval)

			By("Restoring VMSingle from backup")
			vmRestoreImage := "victoriametrics/vmrestore:latest"
			if consts.VMRestoreDefaultImage() != "" {
				vmRestoreImage = fmt.Sprintf("%s:%s", consts.VMRestoreDefaultImage(), consts.VMRestoreDefaultVersion())
			}
			restoreCmd := []string{
				"/vmrestore-prod",
				"-src=fs:///backups/backup1",
				"-storageDataPath=/victoria-metrics-data",
			}

			restorePatch := tests.NewJSONPatchBuilder().
				Add("/spec/volumes", backupVolumes).
				Add("/spec/volumeMounts", backupMounts[:1]).
				Add("/spec/initContainers", []map[string]interface{}{
					{
						"name":         "restore",
						"image":        vmRestoreImage,
						"command":      restoreCmd,
						"volumeMounts": backupMounts,
					},
				}).
				MustBuild()

			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{restorePatch}, consts.ResourceWaitTimeout)

			By("Verifying restored data")
			time.Sleep(consts.DataPropagationDelay)

			_, value, err = prom.VectorScan(ctx, "backup_test_10")
			require.NoError(t, err)
			tests.NewScannedMetric(t, value, "backup_test_10").EqualTo(model.SampleValue(10))
		})
	})

})

var _ = Describe("VPA test", Label("vpa"), func() {
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		var err error
		namespace = tests.RandomNamespace("vm-vpa")
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should create VPA resource for VMSingle when vpa spec is set", Label("id=42a7c221-7696-4240-842a-768dc6a808e9"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
		if !install.OperatorSupportsVMSingleVPA(consts.OperatorImageTag()) {
			Skip("VMSingle vpa spec requires operator >= v0.73.0")
		}

		kubeOpts := nsOpts()

		By("Create VMSingle with VPA spec")
		vpaPatch := tests.NewJSONPatchBuilder().
			Add("/spec/vpa", map[string]interface{}{
				"updatePolicy": map[string]string{"updateMode": "Auto"},
				"resourcePolicy": map[string]interface{}{
					"containerPolicies": []map[string]interface{}{
						{
							"containerName": "vmsingle",
							"maxAllowed":    map[string]string{"cpu": "1", "memory": "1Gi"},
						},
					},
				},
			}).
			MustBuild()

		vmclient := install.GetVMClient(t, kubeOpts)
		install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{vpaPatch}, consts.PollingTimeout)

		By("Verify VPA resource references the VMSingle pod")
		expectResourceNamed(ctx, kubeOpts, "verticalpodautoscaler", "vmsingle")
	})
})

var _ = Describe("Gateway API test", Label("gateway"), func() {
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		var err error
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		waitForGatewayAPIHTTPRouteAccess(ctx, t, kubeOpts)
		namespace = tests.RandomNamespace("vm-gateway")
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		install.DeleteVMAuth(t, kubeOpts, "vmauth")
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should create HTTPRoute resource for VMAuth when httpRoute spec is set", Label("id=eed2da89-e3e6-4b53-bf67-d835155b203f"), func(ctx context.Context) {
		kubeOpts := nsOpts()

		By("Create VMAuth with httpRoute spec")
		httpRoutePatch := tests.NewJSONPatchBuilder().
			Add("/spec/httpRoute", map[string]interface{}{
				"hostnames":  []string{fmt.Sprintf("vmauth.%s.svc.cluster.local", namespace)},
				"parentRefs": []map[string]string{{"name": "default", "namespace": "default"}},
			}).
			MustBuild()

		vmclient := install.GetVMClient(t, kubeOpts)
		install.InstallVMAuth(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{httpRoutePatch})

		By("Verify HTTPRoute resource references the VMAuth")
		expectResourceNamed(ctx, kubeOpts, "httproute", "vmauth")
	})
})

// Enterprise-only specs, gated by Label("enterprise") + the VM_ENTERPRISE
// ginkgo label-filter (see GINKGO_FLAGS in the Makefile).
var _ = Describe("VMAgent Enterprise features", func() {

	var _ = Context("Kafka", func() {
		var testStart time.Time

		BeforeEach(func(ctx context.Context) {
			testStart = time.Now()
			namespace = tests.RandomNamespace("vm")
			var err error
			overwatch, err = tests.SetupOverwatchClient(ctx, t)
			require.NoError(t, err)

			// Namespace must exist before InstallK6 applies namespace-scoped objects.
			nsOpts()

			// Per-namespace k6-operator avoids the shared MaxConcurrentReconciles=1 bottleneck.
			install.InstallK6(ctx, t, namespace)
		})

		AfterEach(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
			install.DeleteVMAgent(t, kubeOpts, "vmagent-producer")
			install.DeleteVMAgent(t, kubeOpts, "vmagent")
			install.DeleteKafka(t, kubeOpts)
			install.DeleteVMCluster(t, kubeOpts, consts.DefaultVMClusterName)
			install.UninstallK6(ctx, t, namespace)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		It("should ingest metrics via Kafka topic",
			Label("enterprise", "id=53a1327f-e029-4a09-aa3d-01d8580fd633"),
			SpecTimeout(consts.VMEnterpriseSpecTimeout),
			func(ctx context.Context) {
				kubeOpts := nsOpts()
				labelEnterpriseNS(ctx, kubeOpts)
				vmclient := install.GetVMClient(t, kubeOpts)

				licensePatch := enterpriseLicensePatch(kubeOpts)

				By("Installing VMCluster in test namespace")
				install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, nil, consts.VMClusterWaitTimeout)

				By("Installing Kafka cluster in test namespace")
				install.InstallKafka(ctx, t, kubeOpts, namespace)

				brokerAddr := install.KafkaBrokerAddr(namespace)
				vmInsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write",
					consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))

				By("Deploying producer VMAgent (relays remote write data to Kafka)")
				producerPatches := append([]jsonpatch.Patch{
					tests.NewJSONPatchBuilder().
						Replace("/metadata/name", "vmagent-producer").
						Add("/spec/remoteWrite", []map[string]interface{}{
							{"url": fmt.Sprintf("kafka://%s/?topic=metrics", brokerAddr)},
						}).
						MustBuild(),
				}, licensePatch)
				install.ApplyVMAgentWithPatches(ctx, t, kubeOpts, namespace, vmclient, "vmagent-producer", producerPatches)

				By("Deploying consumer VMAgent (reads from Kafka, forwards to VMCluster)")
				consumerPatches := append([]jsonpatch.Patch{
					tests.NewJSONPatchBuilder().
						Add("/spec/remoteWrite", []map[string]interface{}{
							{"url": vmInsertURL},
						}).
						WithExtraArg("kafka.consumer.topic", "metrics").
						WithExtraArg("kafka.consumer.topic.brokers", brokerAddr).
						WithExtraArg("kafka.consumer.topic.format", "promremotewrite").
						WithExtraArg("kafka.consumer.topic.groupID", "vmagent-consumer").
						WithExtraArg("kafka.consumer.topic.options", "auto.offset.reset=earliest").
						MustBuild(),
				}, licensePatch)
				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, consumerPatches)

				By("Waiting for Kafka consumer to connect to brokers")
				pollUntilReady(ctx, func() (string, error) {
					return k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts,
						"exec", "deploy/vmagent-vmagent", "-c", "vmagent", "--",
						"sh", "-c",
						"wget -qO- http://localhost:8429/metrics | grep vmagent_kafka_consumer_brokers_up")
				}).WithTimeout(consts.VMClusterWaitTimeout).
					Should(MatchRegexp(`vmagent_kafka_consumer_brokers_up\{[^}]*\} [1-9]`), "kafka consumer not connected to brokers")

				By("Running K6 load test via producer VMAgent")
				producerURL := tests.VMAgentNamedRemoteWriteURL("vmagent-producer", namespace)

				err := install.RunK6Scenario(ctx, t, namespace, consts.DefaultVMClusterName, "kafka-write", 1, "write", map[string]string{
					"VMINSERT_URL":      producerURL,
					"SCENARIO_DURATION": "30s",
				})
				require.NoError(t, err)

				By("Waiting for K6 jobs to complete")
				install.WaitForK6JobsToComplete(ctx, t, namespace, "write", 1, 10*time.Minute)

				tests.WaitForDataPropagation()

				By("Verifying metrics from Kafka appear in VMCluster")
				prom := tenantProm(0)

				labels, _, err := tests.RetryVectorScan(ctx, t, namespace, prom, "k6_metric_0", consts.KafkaRetries)
				require.NoError(t, err)
				require.Equal(t, labels["job"], model.LabelValue("k6_load_test"))
			})
	})

	var _ = Context("VMSingle", func() {
		var testStart time.Time

		BeforeEach(func(ctx context.Context) {
			testStart = time.Now()
			namespace = tests.RandomNamespace("vm")
			var err error
			overwatch, err = tests.SetupOverwatchClient(ctx, t)
			require.NoError(t, err)
		})

		AfterEach(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		Describe("Downsampling", func() {
			It("should downsample data", Label("enterprise", "id=6028448d-69e3-4c55-83f2-111122223333"), SpecTimeout(consts.VMEnterpriseSpecTimeout), func(ctx context.Context) {
				kubeOpts := nsOpts()
				labelEnterpriseNS(ctx, kubeOpts)
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Configure VMSingle with downsampling")
				patch := tests.NewJSONPatchBuilder().
					WithExtraArg("downsampling.period", "0s:1m").
					MustBuild()

				install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch}, consts.ResourceWaitTimeout)

				By("Inserting multiple samples")
				remoteWriter := tests.NewRemoteWriteBuilder().ForVMSingle(namespace)
				for i := 0; i < 5; i++ {
					ts := tests.NewTimeSeriesBuilder("downsample_test").
						WithCount(1).
						WithValue(float64(i)).
						Build()
					err := remoteWriter.Send(ctx, ts)
					require.NoError(t, err)
					time.Sleep(time.Second)
				}

				time.Sleep(time.Minute)

				By("Verifying data is downsampled")
				prom := singleProm()

				_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "count_over_time(downsample_test_0[5m])", 5)
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(1), value, "Expected one sample after downsampling")
			})
		})

		Describe("Retention Filters", func() {
			It("should apply retention filters", Label("enterprise", "id=7028448d-69e3-4c55-83f2-111122223333"), SpecTimeout(consts.VMEnterpriseSpecTimeout), func(ctx context.Context) {
				kubeOpts := nsOpts()
				labelEnterpriseNS(ctx, kubeOpts)
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Configure VMSingle with retention filters")
				patch := tests.NewJSONPatchBuilder().
					WithExtraArg("retentionFilter", `{drop="true"}:5s`).
					MustBuild()

				install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch}, consts.ResourceWaitTimeout)

				By("Inserting data")
				remoteWriter := tests.NewRemoteWriteBuilder().ForVMSingle(namespace)
				tsDrop := tests.NewTimeSeriesBuilder("retention_drop").
					WithCount(1).
					WithValue(1).
					WithLabel("drop", "true").
					Build()
				tsKeep := tests.NewTimeSeriesBuilder("retention_keep").
					WithCount(1).
					WithValue(1).
					WithLabel("drop", "false").
					Build()

				err := remoteWriter.Send(ctx, tsDrop)
				require.NoError(t, err)
				err = remoteWriter.Send(ctx, tsKeep)
				require.NoError(t, err)

				By("Wait for time to pass and trigger retention")
				time.Sleep(time.Minute)

				By("Verifying data")
				prom := singleProm()

				_, value, err := prom.VectorScan(ctx, "retention_drop_0")
				require.EqualError(t, err, consts.ErrNoDataReturned)
				require.Equal(t, model.SampleValue(0), value)

				_, value, err = tests.RetryVectorScan(ctx, t, namespace, prom, "retention_keep_0", 5)
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(1), value)
			})
		})
	})

	var _ = Context("mTLS", func() {
		var testStart time.Time

		BeforeEach(func(ctx context.Context) {
			testStart = time.Now()
			namespace = tests.RandomNamespace("vm")
			var err error
			overwatch, err = tests.SetupOverwatchClient(ctx, t)
			require.NoError(t, err)
		})

		AfterEach(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
			install.DeleteVMAgent(t, kubeOpts, "vmagent-no-client-cert")
			install.DeleteVMAgent(t, kubeOpts, "vmagent")
			install.DeleteVMCluster(t, kubeOpts, consts.DefaultVMClusterName)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		It("should require mTLS for VMAgent remote write to VMCluster",
			Label("enterprise", "id=1ad209d2-2f85-47e3-ae7f-427b687e7f31"),
			SpecTimeout(consts.VMEnterpriseSpecTimeout),
			func(ctx context.Context) {
				kubeOpts := nsOpts()
				labelEnterpriseNS(ctx, kubeOpts)
				vmclient := install.GetVMClient(t, kubeOpts)

				certs, err := newMTLSCerts(namespace)
				require.NoError(t, err)
				require.NoError(t, certs.ApplySecret(ctx, t, kubeOpts, mtlsSecretName))

				licensePatch := enterpriseLicensePatch(kubeOpts)
				vmInsertURL := fmt.Sprintf("https://%s/insert/0/prometheus/api/v1/write",
					consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))
				serverName := fmt.Sprintf("vminsert-%s.%s.svc.cluster.local", consts.DefaultVMClusterName, namespace)

				By("Installing VMCluster with every component protected by mTLS")
				install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{fullMTLSClusterPatch()}, consts.PollingTimeout)

				By("Deploying VMAgent without client certificate")
				badPatches := enterprisePatches(licensePatch,
					tests.NewJSONPatchBuilder().
						Replace("/metadata/name", "vmagent-no-client-cert").
						Add("/spec/remoteWrite", []map[string]interface{}{
							{
								"url": vmInsertURL,
								"tlsConfig": map[string]interface{}{
									"ca":         map[string]interface{}{"secret": map[string]string{"name": mtlsSecretName, "key": "ca.crt"}},
									"serverName": serverName,
								},
							},
						}).
						MustBuild(),
				)
				install.ApplyVMAgentWithPatches(ctx, t, kubeOpts, namespace, vmclient, "vmagent-no-client-cert", badPatches)

				By("Remote-writing metrics to VMAgent without client certificate")
				badTS := tests.NewTimeSeriesBuilder("mtls_rejected").
					WithCount(1).
					WithValue(13).
					WithLabel("source", "mtls").
					Build()
				err = tests.NewRemoteWriteBuilder().
					WithURL(tests.VMAgentNamedRemoteWriteURL("vmagent-no-client-cert", namespace)).
					Send(ctx, badTS)
				require.NoError(t, err)

				By("Deploying VMAgent with client certificate")
				goodPatches := enterprisePatches(licensePatch,
					tests.NewJSONPatchBuilder().
						Add("/spec/secrets", []string{mtlsSecretName}).
						Add("/spec/remoteWrite", []map[string]interface{}{
							{
								"url": vmInsertURL,
								"tlsConfig": map[string]interface{}{
									"ca":         map[string]interface{}{"secret": map[string]string{"name": mtlsSecretName, "key": "ca.crt"}},
									"cert":       map[string]interface{}{"secret": map[string]string{"name": mtlsSecretName, "key": "client.crt"}},
									"keySecret":  map[string]string{"name": mtlsSecretName, "key": "client.key"},
									"serverName": serverName,
								},
							},
						}).
						MustBuild(),
				)
				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, goodPatches)

				By("Remote-writing metrics to VMAgent with client certificate")
				goodTS := tests.NewTimeSeriesBuilder("mtls_accepted").
					WithCount(1).
					WithValue(42).
					WithLabel("source", "mtls").
					Build()
				err = tests.NewRemoteWriteBuilder().
					WithURL(tests.VMAgentRemoteWriteURL(namespace)).
					Send(ctx, goodTS)
				require.NoError(t, err)

				tests.WaitForDataPropagation()

				By("Verifying VMSelect accepts queries only with client certificate")
				installMTLSCurlPod(ctx, t, kubeOpts)
				_, err = runVMSelectQueryFromCurlPod(ctx, t, kubeOpts, namespace, "1", false)
				require.Error(t, err)
				out, err := runVMSelectQueryFromCurlPod(ctx, t, kubeOpts, namespace, "mtls_accepted_0", true)
				require.NoError(t, err)
				require.Contains(t, out, `"status":"success"`)
				require.Contains(t, out, `"mtls_accepted_0"`)
				require.Contains(t, out, `"source":"mtls"`)

				out, err = runVMSelectQueryFromCurlPod(ctx, t, kubeOpts, namespace, "mtls_rejected_0", true)
				require.NoError(t, err)
				require.NotContains(t, out, `"mtls_rejected_0"`)
			})
	})

})

func enterpriseLicensePatch(kubeOpts *k8s.KubectlOptions) jsonpatch.Patch {
	if consts.LicenseFile() == "" {
		return nil
	}
	secretYaml, err := consts.PrepareLicenseSecret(namespace)
	require.NoError(t, err)
	k8s.KubectlApplyFromString(t, kubeOpts, secretYaml)
	patch, err := install.LicensePatch()
	require.NoError(t, err)
	return patch
}

func enterprisePatches(licensePatch jsonpatch.Patch, patches ...jsonpatch.Patch) []jsonpatch.Patch {
	if len(licensePatch) == 0 {
		return patches
	}
	return append(patches, licensePatch)
}

func httpMTLSArgs() map[string]string {
	secretPath := "/etc/vm/secrets/" + mtlsSecretName
	return map[string]string{
		"tls":         "true",
		"tlsCertFile": secretPath + "/server.crt",
		"tlsKeyFile":  secretPath + "/server.key",
		"mtls":        "true",
		"mtlsCAFile":  secretPath + "/ca.crt",
	}
}

func fullMTLSClusterPatch() jsonpatch.Patch {
	componentArgs := httpMTLSArgs()
	maps.Copy(componentArgs, clusterTLSArgs())

	// TLS breaks HTTP probes, so every component is probed on its own TCP port.
	components := []struct {
		name string
		port int
	}{{"vminsert", 8480}, {"vmselect", 8481}, {"vmstorage", 8482}}

	patch := tests.NewJSONPatchBuilder()
	for _, component := range components {
		path := "/spec/" + component.name
		patch.Add(path+"/secrets", []string{mtlsSecretName}).
			Add(path+"/extraArgs", componentArgs).
			Add(path+"/readinessProbe", tests.TCPProbe(component.port)).
			Add(path+"/livenessProbe", tests.TCPProbe(component.port)).
			Add(path+"/startupProbe", tests.TCPProbe(component.port))
	}
	return patch.MustBuild()
}

func clusterTLSArgs() map[string]string {
	secretPath := "/etc/vm/secrets/" + mtlsSecretName
	return map[string]string{
		"cluster.tls":         "true",
		"cluster.tlsCertFile": secretPath + "/server.crt",
		"cluster.tlsKeyFile":  secretPath + "/server.key",
		"cluster.tlsCAFile":   secretPath + "/ca.crt",
	}
}

func installMTLSCurlPod(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) {
	install.KubectlApplyFromString(ctx, t, kubeOpts, fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: vmselect-mtls-client
spec:
  restartPolicy: Never
  containers:
  - name: curl
    image: curlimages/curl:8.8.0
    command: ["sleep", "3600"]
    volumeMounts:
    - name: mtls
      mountPath: /mtls
      readOnly: true
  volumes:
  - name: mtls
    secret:
      secretName: %s
`, mtlsSecretName))
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pod/vmselect-mtls-client",
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))
}

func runVMSelectQueryFromCurlPod(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, query string, withClientCert bool) (string, error) {
	args := []string{
		"exec", "pod/vmselect-mtls-client", "-c", "curl", "--",
		"curl", "--fail", "--silent", "--show-error",
		"--cacert", "/mtls/ca.crt",
	}
	if withClientCert {
		args = append(args, "--cert", "/mtls/client.crt", "--key", "/mtls/client.key")
	}
	args = append(args,
		"--data-urlencode", "query="+query,
		fmt.Sprintf("https://%s/select/0/prometheus/api/v1/query", consts.GetVMSelectSvc(consts.DefaultVMClusterName, namespace)),
	)
	return k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, args...)
}

func newMTLSCerts(namespace string) (tests.MTLSCerts, error) {
	cluster := consts.DefaultVMClusterName
	dnsNames := tests.ServiceDNSNames(namespace,
		"vminsert-"+cluster, "vmselect-"+cluster, "vmstorage-"+cluster, "*.vmstorage-"+cluster)

	return tests.NewMTLSCerts("vm-mtls-ca", "vmcluster", "vmagent", dnsNames)
}
