package vmcluster_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"

	jsonpatch "github.com/evanphx/json-patch/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestVMClusterTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	// suiteConfig.FocusStrings = []string{"should relabel data sent via remote write"}
	RunSpecs(t, "VMCluster test Suite", suiteConfig, reporterConfig)
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
			consts.SmokeValuesFile,
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

		install.DeleteVMCluster(t, kubeOpts, namespace)
		tests.CleanupNamespace(t, kubeOpts, namespace)

		tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)
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

			tests.WaitForDataPropagation()

			By("Verifying tenant 0 data is isolated")
			tenant0Prom := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(0).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tenant0Prom.VectorScan(ctx, "foo_2")
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

			_, value, err = tenant1Prom.VectorScan(ctx, "bar_2")
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

			_, value, err = multitenantProm.VectorScan(ctx, "foo_2")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))

			_, value, err = multitenantProm.VectorScan(ctx, "bar_2")
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

			tests.WaitForDataPropagation()

			By("Verifying tenant 0 data is isolated")
			tenant0Prom := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(0).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := tenant0Prom.VectorScan(ctx, "foo_2")
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

			_, value, err = tenant1Prom.VectorScan(ctx, "bar_2")
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

			tests.WaitForDataPropagation()

			By("Verifying data can be retrieved via multitenant URL")
			multitenantProm := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				Multitenant().
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := multitenantProm.VectorScan(ctx, "foo_2")
			require.NoError(t, err)
			require.Equal(t, value, model.SampleValue(1))

			_, value, err = multitenantProm.VectorScan(ctx, "bar_2")
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

			tests.WaitForDataPropagation()

			By("foo has cluster=dev label")
			tenantProm := tests.NewPromClientBuilder().
				WithNamespace(namespace).
				WithTenant(0).
				WithStartTime(overwatch.Start).
				MustBuild()

			labels, value, err := tenantProm.VectorScan(ctx, "foo_2")
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

			_, value, err := prom.VectorScan(ctx, "sum_over_time(cluster_aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "influx_test_value")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "influx_vminsert_test_value")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "datadog.test.metric")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "datadog.vminsert.test.metric")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "otel_test_metric")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "otel_vmagent_test_metric")
				require.NoError(t, err)
				require.Equal(t, value, model.SampleValue(456))
				require.Equal(t, labels["foo"], model.LabelValue("baz"))
			})
		})
	})
})
