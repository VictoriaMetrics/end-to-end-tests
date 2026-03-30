package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestFunctionalTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Functional test Suite", suiteConfig, reporterConfig)
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
			context.Background(),
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
	}, func(ctx context.Context) {
		t = tests.GetT()
		namespace = tests.RandomNamespace("vm")
	},
)

var _ = Describe("VMCluster test", Label("vmcluster"), func() {
	BeforeEach(func(ctx context.Context) {
		var err error
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		// Create new VMCluster object
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		vmclient := install.GetVMClient(t, kubeOpts)
		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{})

		c = tests.NewHTTPClient()
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)

		install.DeleteVMCluster(t, kubeOpts, namespace)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	Describe("Multitenancy", func() {
		It("should not mix data sent to different tenants", Label("id=66618081-b150-4b48-8180-ae1f53512117"), func(ctx context.Context) {
			// Build remote write helpers for each tenant
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
			err := tenant0Writer.Send(fooTimeSeries)
			require.NoError(t, err)

			By("Inserting data into tenant 1")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = tenant1Writer.Send(barTimeSeries)
			require.NoError(t, err)

			By("Verifying tenant 0 data is isolated")
			tenant0Prom := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(0).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, tenant0Prom, "foo_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))

			_, value, err = tenant0Prom.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))

			By("Verifying tenant 1 data is isolated")
			tenant1Prom := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(1).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, tenant1Prom, "bar_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(5))

			_, value, err = tenant1Prom.VectorScan(ctx, "foo_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))

			By("Verifying data can be retrieved via multitenant URL")
			multitenantProm := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				Multitenant().
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "foo_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "bar_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(5))
		})

		It("should accept data via multitenant URL", Label("id=16c08934-9e25-45ed-a94b-4fbbbe3170ef"), func(ctx context.Context) {
			// Build remote write helper for multitenant endpoint
			multitenantWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForMultitenant(namespace)

			By("Inserting data into tenant 0 via multitenant endpoint")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				WithTenantLabel(0).
				Build()
			err := multitenantWriter.Send(fooTimeSeries)
			require.NoError(t, err)

			By("Inserting data into tenant 1 via multitenant endpoint")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				WithTenantLabel(1).
				Build()
			err = multitenantWriter.Send(barTimeSeries)
			require.NoError(t, err)

			By("Verifying tenant 0 data is isolated")
			tenant0Prom := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(0).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, tenant0Prom, "foo_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))

			_, value, err = tenant0Prom.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))

			By("Verifying tenant 1 data is isolated")
			tenant1Prom := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(1).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, tenant1Prom, "bar_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(5))

			_, value, err = tenant1Prom.VectorScan(ctx, "foo_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))
		})

		It("should retrieve data from different tenants via multitenant URL", Label("id=7e075898-f6c4-49d5-9d7f-8a6163759065"), func(ctx context.Context) {
			// Build remote write helpers for each tenant
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
			err := tenant0Writer.Send(fooTimeSeries)
			require.NoError(t, err)

			By("Inserting data into tenant 1")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = tenant1Writer.Send(barTimeSeries)
			require.NoError(t, err)

			By("Verifying data can be retrieved via multitenant URL")
			multitenantProm := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				Multitenant().
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "foo_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))

			_, value, err = tests.RetryVectorScan(ctx, t, namespace, multitenantProm, "bar_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(5))
		})
	})

	Describe("Relabeling", func() {
		It("should relabel data sent via remote write", Label("id=e72f26ba-c1b7-4671-9c7e-7cfa630c33a9"), func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.EnsureNamespaceExists(t, kubeOpts, namespace)
			vmclient := install.GetVMClient(t, kubeOpts)

			By("Configure VMAgent to relabel data")
			vmInsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write", consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))

			// Create inline relabel config for VMAgent patch
			patchOps := []install.PatchOp{
				{
					Op:   "add",
					Path: "/spec/remoteWrite",
					Value: []map[string]interface{}{
						{
							"url": vmInsertURL,
							"inlineUrlRelabelConfig": []map[string]interface{}{
								{
									"target_label": "cluster",
									"replacement":  "dev",
								},
								{
									"action":        "drop",
									"source_labels": []string{"__name__"},
									"regex":         "bar_.*",
								},
							},
						},
					},
				},
			}
			patch, err := install.CreateJsonPatch(patchOps)
			require.NoError(t, err)

			install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})
			install.ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)

			// Build remote write helper for VMAgent
			vmagentWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMAgent(namespace)

			By("Inserting foo data via VMAgent (should be relabeled)")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				Build()
			err = vmagentWriter.Send(fooTimeSeries)
			require.NoError(t, err)

			By("Inserting bar data (should be dropped)")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = vmagentWriter.Send(barTimeSeries)
			require.NoError(t, err)

			By("foo has cluster=dev label")
			tenantProm := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(0).
				WithStartTime(overwatch.Start).
				MustBuild()

			labels, value, err := tests.RetryVectorScan(ctx, t, namespace, tenantProm, "foo_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))
			require.Contains(t, labels, model.LabelName("cluster"))
			require.Equal(t, labels["cluster"], model.LabelValue("dev"))

			By("bar_2 was removed")
			_, value, err = tenantProm.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))
		})
	})

	Describe("Streaming Aggregation", func() {
		It("should aggregate data with sum_samples output via VMAgent", Label("id=c3d4e5f6-a7b8-9012-cdef-345678901234"), func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.EnsureNamespaceExists(t, kubeOpts, namespace)
			vmclient := install.GetVMClient(t, kubeOpts)

			By("Configure VMAgent with streaming aggregation")
			vmInsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write", consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))

			patchOps := []install.PatchOp{
				{
					Op:   "add",
					Path: "/spec/remoteWrite",
					Value: []map[string]interface{}{
						{
							"url": vmInsertURL,
							"streamAggrConfig": map[string]interface{}{
								"rules": []map[string]interface{}{
									{
										"match":    []string{`{__name__=~"cluster_aggr_.*"}`},
										"interval": "30s",
										"outputs":  []string{"sum_samples"},
										"without":  []string{"foo", "bar", "baz"},
									},
								},
							},
						},
					},
				},
			}
			patch, err := install.CreateJsonPatch(patchOps)
			require.NoError(t, err)

			install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})
			install.ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)

			// Build remote write helper for VMAgent
			vmagentWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMAgent(namespace)

			By("Inserting multiple samples for aggregation")
			for i := 0; i < 5; i++ {
				aggrTimeSeries := tests.NewTimeSeriesBuilder("cluster_aggr_test").
					WithCount(3).
					WithValue(1).
					Build()
				err = vmagentWriter.Send(aggrTimeSeries)
				require.NoError(t, err)
				time.Sleep(2 * time.Second)
			}

			By("Inserting non-matching metrics")
			nonAggrTimeSeries := tests.NewTimeSeriesBuilder("cluster_nonaggr").
				WithCount(3).
				WithValue(100).
				Build()
			err = vmagentWriter.Send(nonAggrTimeSeries)
			require.NoError(t, err)

			By("Waiting for aggregation interval to pass")
			tests.WaitForAggregation()

			By("Verifying aggregated metrics exist with correct naming")
			prom := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(0).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "sum_over_time(cluster_aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(5))

			By("Verifying non-matching metrics are written as-is")
			_, value, err = prom.VectorScan(ctx, "cluster_nonaggr_0")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(100))

			By("Verifying original aggr metrics are dropped")
			_, value, err = prom.VectorScan(ctx, "cluster_aggr_test_0")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))
		})
	})

	Describe("Ingestion", func() {
		Context("InfluxDB", func() {
			It("should ingest data via influxdb protocol to vmagent", Label("id=e5fba904-59b8-4440-97d5-9747dc78f959"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Configure VMAgent to write to VMCluster")
				vmInsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write", consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))

				patchOps := []install.PatchOp{
					{
						Op:   "add",
						Path: "/spec/remoteWrite",
						Value: []map[string]interface{}{
							{
								"url": vmInsertURL,
							},
						},
					},
				}
				patch, err := install.CreateJsonPatch(patchOps)
				require.NoError(t, err)

				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})
				install.ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)

				By("Inserting data via InfluxDB protocol")
				influxURL := fmt.Sprintf("http://%s/write", consts.VMAgentNamespacedHost(namespace))
				data := "influx_test,foo=bar value=123"
				resp, err := c.Post(influxURL, "", strings.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, http.StatusNoContent, resp.StatusCode)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "influx_test_value", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
			})

			It("should ingest data via influxdb protocol to vminsert", Label("id=11223344-5566-7788-9900-aabbccddeeff"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)

				By("Inserting data via InfluxDB protocol")
				influxURL := fmt.Sprintf("http://%s/insert/0/influx/write", consts.VMInsertHost(namespace))
				data := "influx_vminsert_test,foo=bar value=123"
				resp, err := c.Post(influxURL, "", strings.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, http.StatusNoContent, resp.StatusCode)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "influx_vminsert_test_value", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
			})
		})

		Context("Datadog", func() {
			It("should ingest data via datadog protocol to vmagent", Label("id=6862ebb3-0d9f-4af1-9359-08692c8dfc5c"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Configure VMAgent to write to VMCluster")
				vmInsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write", consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))

				patchOps := []install.PatchOp{
					{
						Op:   "add",
						Path: "/spec/remoteWrite",
						Value: []map[string]interface{}{
							{
								"url": vmInsertURL,
							},
						},
					},
				}
				patch, err := install.CreateJsonPatch(patchOps)
				require.NoError(t, err)

				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})
				install.ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)

				By("Inserting data via Datadog protocol")
				datadogURL := fmt.Sprintf("http://%s/datadog/api/v1/series", consts.VMAgentNamespacedHost(namespace))
				now := time.Now().Unix()
				ddSeries := tests.DatadogSeries{
					Series: []tests.DatadogMetric{
						{
							Metric: "datadog.test.metric",
							Points: [][]interface{}{
								{now, 123},
							},
							Tags: []string{
								"env:test",
								"foo:bar",
							},
							Host: "test-host",
							Type: "gauge",
						},
					},
				}
				data, err := json.Marshal(ddSeries)
				require.NoError(t, err)

				resp, err := c.Post(datadogURL, "application/json", bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, resp.StatusCode, http.StatusAccepted)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "datadog.test.metric", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["env"], model.LabelValue("test"))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
				require.Equal(t, labels["host"], model.LabelValue("test-host"))
			})

			It("should ingest data via datadog protocol to vminsert", Label("id=aabbccdd-1122-3344-5566-77889900aabb"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)

				By("Inserting data via Datadog protocol")
				datadogURL := fmt.Sprintf("http://%s/insert/0/datadog/api/v1/series", consts.VMInsertHost(namespace))
				now := time.Now().Unix()
				ddSeries := tests.DatadogSeries{
					Series: []tests.DatadogMetric{
						{
							Metric: "datadog.vminsert.test.metric",
							Points: [][]interface{}{
								{now, 123},
							},
							Tags: []string{
								"env:test",
								"foo:bar",
							},
							Host: "test-host",
							Type: "gauge",
						},
					},
				}
				data, err := json.Marshal(ddSeries)
				require.NoError(t, err)

				resp, err := c.Post(datadogURL, "application/json", bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, http.StatusAccepted, resp.StatusCode)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "datadog.vminsert.test.metric", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["env"], model.LabelValue("test"))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
				require.Equal(t, labels["host"], model.LabelValue("test-host"))
			})
		})

		Context("OpenTelemetry", func() {
			It("should ingest data via opentelemetry protocol to vminsert", Label("id=4e7c8581-2c93-4796-9817-219586111111"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)

				By("Inserting data via OpenTelemetry protocol")
				otelURL := fmt.Sprintf("http://%s/insert/0/opentelemetry/v1/metrics", consts.VMInsertHost(namespace))

				timestamp := time.Now().UnixNano()

				// Construct OTLP Protobuf payload
				req := &colmetricspb.ExportMetricsServiceRequest{
					ResourceMetrics: []*metricspb.ResourceMetrics{
						{
							ScopeMetrics: []*metricspb.ScopeMetrics{
								{
									Metrics: []*metricspb.Metric{
										{
											Name: "otel_test_metric",
											Data: &metricspb.Metric_Gauge{
												Gauge: &metricspb.Gauge{
													DataPoints: []*metricspb.NumberDataPoint{
														{
															TimeUnixNano: uint64(timestamp),
															Value: &metricspb.NumberDataPoint_AsInt{
																AsInt: 123,
															},
															Attributes: []*commonpb.KeyValue{
																{
																	Key: "foo",
																	Value: &commonpb.AnyValue{
																		Value: &commonpb.AnyValue_StringValue{
																			StringValue: "bar",
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				}

				data, err := proto.Marshal(req)
				require.NoError(t, err)

				resp, err := c.Post(otelURL, "application/x-protobuf", bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "otel_test_metric", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
			})

			It("should ingest data via opentelemetry protocol to vmagent", Label("id=55667788-9900-aabb-ccdd-eeff11223344"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Configure VMAgent to write to VMCluster")
				vmInsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write", consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))

				patchOps := []install.PatchOp{
					{
						Op:   "add",
						Path: "/spec/remoteWrite",
						Value: []map[string]interface{}{
							{
								"url": vmInsertURL,
							},
						},
					},
				}
				patch, err := install.CreateJsonPatch(patchOps)
				require.NoError(t, err)

				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})
				install.ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)

				By("Inserting data via OpenTelemetry protocol")
				otelURL := fmt.Sprintf("http://%s/opentelemetry/v1/metrics", consts.VMAgentNamespacedHost(namespace))

				timestamp := time.Now().UnixNano()

				// Construct OTLP Protobuf payload
				req := &colmetricspb.ExportMetricsServiceRequest{
					ResourceMetrics: []*metricspb.ResourceMetrics{
						{
							ScopeMetrics: []*metricspb.ScopeMetrics{
								{
									Metrics: []*metricspb.Metric{
										{
											Name: "otel_vmagent_test_metric",
											Data: &metricspb.Metric_Gauge{
												Gauge: &metricspb.Gauge{
													DataPoints: []*metricspb.NumberDataPoint{
														{
															TimeUnixNano: uint64(timestamp),
															Value: &metricspb.NumberDataPoint_AsInt{
																AsInt: 456,
															},
															Attributes: []*commonpb.KeyValue{
																{
																	Key: "foo",
																	Value: &commonpb.AnyValue{
																		Value: &commonpb.AnyValue_StringValue{
																			StringValue: "baz",
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				}

				data, err := proto.Marshal(req)
				require.NoError(t, err)

				resp, err := c.Post(otelURL, "application/x-protobuf", bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "otel_vmagent_test_metric", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(456))
				require.Equal(t, labels["foo"], model.LabelValue("baz"))
			})
		})
	})
})

var _ = Describe("VMSingle test", Label("vmsingle"), func() {
	BeforeEach(func(ctx context.Context) {
		var err error
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		c = tests.NewHTTPClient()
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)
		install.DeleteVMSingle(t, kubeOpts, namespace)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	Describe("Relabeling", func() {
		It("should relabel data sent via remote write", Label("id=aabbccdd-eeff-0011-2233-445566778899"), func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.EnsureNamespaceExists(t, kubeOpts, namespace)

			By("Configure VMSingle to relabel data")
			cfgMapName := "vmsingle-relabel-config"

			// Build relabel config using builder
			relabelConfig := tests.NewRelabelConfigBuilder().
				AddLabel("cluster", "dev").
				DropByName("bar_.*").
				MustBuild()

			// Build and apply ConfigMap using builder
			err := tests.NewConfigMapBuilder(cfgMapName).
				WithRelabelConfig(relabelConfig).
				Apply(t, kubeOpts)
			require.NoError(t, err)

			// Build JSON patch using builder
			patch := tests.NewJSONPatchBuilder().
				WithVMSingleConfig(cfgMapName, "relabelConfig", "relabel.yml").
				MustBuild()

			vmclient := install.GetVMClient(t, kubeOpts)
			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})

			// Build remote write helper
			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			By("Inserting foo data (should be relabeled)")
			fooTimeSeries := tests.NewTimeSeriesBuilder("foo").
				WithCount(10).
				WithValue(1).
				Build()
			err = remoteWriter.Send(fooTimeSeries)
			require.NoError(t, err)

			By("Inserting bar data (should be dropped)")
			barTimeSeries := tests.NewTimeSeriesBuilder("bar").
				WithCount(10).
				WithValue(5).
				Build()
			err = remoteWriter.Send(barTimeSeries)
			require.NoError(t, err)

			By("foo has cluster=dev label")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "foo_2", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))
			require.Contains(t, labels, model.LabelName("cluster"))
			require.Equal(t, labels["cluster"], model.LabelValue("dev"))

			By("bar_2 was removed")
			_, value, err = prom.VectorScan(ctx, "bar_2")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))
		})
	})

	Describe("Streaming Aggregation", func() {
		It("should aggregate data with sum_samples output", Label("id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"), func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.EnsureNamespaceExists(t, kubeOpts, namespace)

			By("Configure VMSingle with streaming aggregation")
			cfgMapName := "vmsingle-stream-aggr-config"

			// Build streaming aggregation config using builder
			streamAggrConfig := tests.NewStreamAggrConfigBuilder().
				AddRule(`{__name__=~"aggr_.*"}`, "30s", []string{"sum_samples"}).
				WithoutLabels("foo", "bar", "baz").
				MustBuild()

			// Build and apply ConfigMap using builder
			err := tests.NewConfigMapBuilder(cfgMapName).
				WithStreamAggrConfig(streamAggrConfig).
				Apply(t, kubeOpts)
			require.NoError(t, err)

			// Build JSON patch using builder
			patch := tests.NewJSONPatchBuilder().
				WithVMSingleConfig(cfgMapName, "streamAggr.config", "stream-aggr.yml").
				MustBuild()

			vmclient := install.GetVMClient(t, kubeOpts)
			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})

			// Build remote write helper
			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			By("Inserting multiple samples for aggregation")
			for i := 0; i < 5; i++ {
				aggrTimeSeries := tests.NewTimeSeriesBuilder("aggr_test").
					WithCount(3).
					WithValue(1).
					Build()
				err = remoteWriter.Send(aggrTimeSeries)
				require.NoError(t, err)
				time.Sleep(2 * time.Second)
			}

			By("Inserting non-matching metrics")
			nonAggrTimeSeries := tests.NewTimeSeriesBuilder("nonaggr").
				WithCount(3).
				WithValue(100).
				Build()
			err = remoteWriter.Send(nonAggrTimeSeries)
			require.NoError(t, err)

			By("Waiting for aggregation interval to pass")
			tests.WaitForAggregation()

			By("Verifying aggregated metrics exist with correct naming")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "sum_over_time(aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(5))

			By("Verifying non-matching metrics are written as-is")
			_, value, err = prom.VectorScan(ctx, "nonaggr_0")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(100))

			By("Verifying original aggr metrics are dropped")
			_, value, err = prom.VectorScan(ctx, "aggr_test_0")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, value, model.SampleValue(0))
		})
	})

	Describe("Ingestion", func() {
		Context("InfluxDB", func() {
			It("should ingest data via influxdb protocol", Label("id=b2c3d4e5-f6a7-8901-ba12-345678901234"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)

				vmclient := install.GetVMClient(t, kubeOpts)
				install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, nil)

				By("Inserting data via InfluxDB protocol")
				influxURL := fmt.Sprintf("http://%s/write", consts.VMSingleNamespacedHost(namespace))
				data := "influx_test,foo=bar value=123"
				resp, err := c.Post(influxURL, "", strings.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, http.StatusNoContent, resp.StatusCode)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "influx_test_value", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
			})
		})

		Context("Datadog", func() {
			It("should ingest data via datadog protocol", Label("id=905d5353-b40f-4822-a2ab-decd29f1ac12"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)

				vmclient := install.GetVMClient(t, kubeOpts)
				install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, nil)

				By("Inserting data via Datadog protocol")
				datadogURL := fmt.Sprintf("http://%s/datadog/api/v1/series", consts.VMSingleNamespacedHost(namespace))
				now := time.Now().Unix()
				ddSeries := tests.DatadogSeries{
					Series: []tests.DatadogMetric{
						{
							Metric: "datadog.test.metric",
							Points: [][]interface{}{
								{now, 123},
							},
							Tags: []string{
								"env:test",
								"foo:bar",
							},
							Host: "test-host",
							Type: "gauge",
						},
					},
				}
				data, err := json.Marshal(ddSeries)
				require.NoError(t, err)

				resp, err := c.Post(datadogURL, "application/json", bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, resp.StatusCode, http.StatusAccepted)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "datadog.test.metric", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["env"], model.LabelValue("test"))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
				require.Equal(t, labels["host"], model.LabelValue("test-host"))
			})
		})

		Context("OpenTelemetry", func() {
			It("should ingest data via opentelemetry protocol", Label("id=55ca0534-1111-2222-3333-444455556666"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)

				vmclient := install.GetVMClient(t, kubeOpts)
				install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, nil)

				By("Inserting data via OpenTelemetry protocol")
				otelURL := fmt.Sprintf("http://%s/opentelemetry/v1/metrics", consts.VMSingleNamespacedHost(namespace))

				timestamp := time.Now().UnixNano()

				// Construct OTLP Protobuf payload
				req := &colmetricspb.ExportMetricsServiceRequest{
					ResourceMetrics: []*metricspb.ResourceMetrics{
						{
							ScopeMetrics: []*metricspb.ScopeMetrics{
								{
									Metrics: []*metricspb.Metric{
										{
											Name: "otel_test_metric",
											Data: &metricspb.Metric_Gauge{
												Gauge: &metricspb.Gauge{
													DataPoints: []*metricspb.NumberDataPoint{
														{
															TimeUnixNano: uint64(timestamp),
															Value: &metricspb.NumberDataPoint_AsInt{
																AsInt: 123,
															},
															Attributes: []*commonpb.KeyValue{
																{
																	Key: "foo",
																	Value: &commonpb.AnyValue{
																		Value: &commonpb.AnyValue_StringValue{
																			StringValue: "bar",
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				}

				data, err := proto.Marshal(req)
				require.NoError(t, err)

				resp, err := c.Post(otelURL, "application/x-protobuf", bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				_ = resp.Body.Close()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "otel_test_metric", 5)
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(123))
				require.Equal(t, labels["foo"], model.LabelValue("bar"))
			})
		})
	})

	Describe("Backup and Restore", func() {
		It("should backup and restore data via PVC", Label("id=8576d108-7357-4555-b4fa-7e8649186c07"), func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.EnsureNamespaceExists(t, kubeOpts, namespace)

			vmclient := install.GetVMClient(t, kubeOpts)

			By("Creating backup PVC")
			backupPVCName := "backup-pvc"
			k8s.KubectlApply(t, kubeOpts, consts.ManifestsRoot()+"/backup-pvc.yaml")

			By("Installing VMSingle")
			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, nil)

			By("Sending data")
			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			ts := tests.NewTimeSeriesBuilder("backup_test").
				WithCount(100).
				WithValue(10).
				Build()
			err := remoteWriter.Send(ts)
			require.NoError(t, err)

			By("Verifying data before backup")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "backup_test_10", 5)
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(10))

			By("Reconfiguring VMSingle with backup sidecar")
			vmBackupImage := "victoriametrics/vmbackup:latest"
			if consts.VMBackupDefaultImage() != "" {
				vmBackupImage = fmt.Sprintf("%s:%s", consts.VMBackupDefaultImage(), consts.VMBackupDefaultVersion())
			}
			ops := []map[string]interface{}{
				{
					"op":   "add",
					"path": "/spec/volumes",
					"value": []map[string]interface{}{
						{
							"name": "backups",
							"persistentVolumeClaim": map[string]string{
								"claimName": backupPVCName,
							},
						},
					},
				},
				{
					"op":   "add",
					"path": "/spec/containers",
					"value": []map[string]interface{}{
						{
							"name":    "vmbackup",
							"image":   vmBackupImage,
							"command": []string{"tail", "-f", "/dev/null"},
							"volumeMounts": []map[string]string{
								{
									"name":      "backups",
									"mountPath": "/backups",
								},
								{
									"name":      "data",
									"mountPath": "/victoria-metrics-data",
								},
							},
						},
					},
				},
			}
			patchBytes, err := json.Marshal(ops)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(patchBytes)
			require.NoError(t, err)

			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})
			k8s.WaitUntilNumPodsCreated(t, kubeOpts, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=vmsingle,app.kubernetes.io/instance=vmsingle"}, 1, consts.Retries, consts.PollingInterval)

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
			fmt.Println("Executing backup command:", backupContainerCmd)
			k8s.RunKubectl(t, kubeOpts, backupContainerCmd...)

			By("Destroying VMSingle")
			install.DeleteVMSingle(t, kubeOpts, "vmsingle")
			k8s.WaitUntilNumPodsCreated(t, kubeOpts, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=vmsingle,app.kubernetes.io/instance=vmsingle"}, 0, consts.Retries, consts.PollingInterval)

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

			initContainer := map[string]interface{}{
				"name":    "restore",
				"image":   vmRestoreImage,
				"command": restoreCmd,
				"volumeMounts": []map[string]string{
					{
						"name":      "backups",
						"mountPath": "/backups",
					},
					{
						"name":      "data",
						"mountPath": "/victoria-metrics-data",
					},
				},
			}

			restoreOps := []map[string]interface{}{
				{
					"op":   "add",
					"path": "/spec/volumes",
					"value": []map[string]interface{}{
						{
							"name": "backups",
							"persistentVolumeClaim": map[string]string{
								"claimName": backupPVCName,
							},
						},
					},
				},
				{
					"op":   "add",
					"path": "/spec/volumeMounts",
					"value": []map[string]interface{}{
						{
							"name":      "backups",
							"mountPath": "/backups",
						},
					},
				},
				{
					"op":    "add",
					"path":  "/spec/initContainers",
					"value": []interface{}{initContainer},
				},
			}

			patchBytes, err = json.Marshal(restoreOps)
			require.NoError(t, err)
			restorePatch, err := jsonpatch.DecodePatch(patchBytes)
			require.NoError(t, err)

			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{restorePatch})

			By("Verifying restored data")
			time.Sleep(consts.DataPropagationDelay)

			_, value, err = prom.VectorScan(ctx, "backup_test_10")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(10))
		})
	})

	Describe("Downsampling", func() {
		It("should downsample data", Label("enterprise", "id=6028448d-69e3-4c55-83f2-111122223333"), func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.EnsureNamespaceExists(t, kubeOpts, namespace)

			vmclient := install.GetVMClient(t, kubeOpts)

			By("Configure VMSingle with downsampling")
			// Downsample everything (offset 0s) to 1m resolution
			patch := tests.NewJSONPatchBuilder().
				WithExtraArg("downsampling.period", "0s:1m").
				MustBuild()

			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})

			By("Inserting multiple samples")
			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			// Write 5 samples for the same series
			for i := 0; i < 5; i++ {
				ts := tests.NewTimeSeriesBuilder("downsample_test").
					WithCount(1).
					WithValue(float64(i)).
					Build()
				err := remoteWriter.Send(ts)
				require.NoError(t, err)
				time.Sleep(time.Second)
			}

			// Wait a bit for merge to complete
			time.Sleep(1 * time.Minute)

			By("Verifying data is downsampled")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "count_over_time(downsample_test_0[5m])", 5)
			require.NoError(t, err)
			require.Equal(t, model.SampleValue(1), value, "Expected one sample after downsampling")
			_ = labels
		})
	})

	Describe("Retention Filters", func() {
		It("should apply retention filters", Label("enterprise", "id=7028448d-69e3-4c55-83f2-111122223333"), func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.EnsureNamespaceExists(t, kubeOpts, namespace)

			vmclient := install.GetVMClient(t, kubeOpts)

			By("Configure VMSingle with retention filters")
			// Create retention filter config
			// Drop data with label drop="true" after 1s
			patch := tests.NewJSONPatchBuilder().
				WithExtraArg("retentionFilter", `{drop="true"}:5s`).
				MustBuild()

			install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})

			By("Inserting data")
			remoteWriter := tests.NewRemoteWriteBuilder().
				WithHTTPClient(c).
				ForVMSingle(namespace)

			// Series to be dropped
			tsDrop := tests.NewTimeSeriesBuilder("retention_drop").
				WithCount(1).
				WithValue(1).
				WithLabel("drop", "true").
				Build()

			// Series to keep
			tsKeep := tests.NewTimeSeriesBuilder("retention_keep").
				WithCount(1).
				WithValue(1).
				WithLabel("drop", "false").
				Build()

			err := remoteWriter.Send(tsDrop)
			require.NoError(t, err)

			err = remoteWriter.Send(tsKeep)
			require.NoError(t, err)

			By("Wait for time to pass and trigger retention")
			time.Sleep(1 * time.Minute)

			By("Verifying data")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			// Check dropped data
			_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "retention_drop_0", 5)
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, model.SampleValue(0), value)

			// Check kept data
			_, value, err = prom.VectorScan(ctx, "retention_keep_0")
			require.NoError(t, err)
			require.Equal(t, model.SampleValue(1), value)
		})
	})
})
