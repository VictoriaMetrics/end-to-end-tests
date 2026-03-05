package vmsingle_test

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

func TestVMSingleTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	// suiteConfig.FocusStrings = []string{"should backup and restore data via PVC"}
	RunSpecs(t, "VMSingle test Suite", suiteConfig, reporterConfig)
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

		// Remove stock VMCluster
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.DeleteVMCluster(t, kubeOpts, consts.DefaultReleaseName)
	}, func(ctx context.Context) {
		t = tests.GetT()
		namespace = tests.RandomNamespace("vm")
	},
)

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

			tests.WaitForDataPropagation()

			By("foo has cluster=dev label")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			labels, value, err := prom.VectorScan(ctx, "foo_2")
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

			_, value, err := prom.VectorScan(ctx, "sum_over_time(aggr_test_0:30s_without_bar_baz_foo_sum_samples[5m])")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "influx_test_value")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "datadog.test.metric")
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

				tests.WaitForDataPropagation()

				By("Verifying data via Prometheus protocol")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := prom.VectorScan(ctx, "otel_test_metric")
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
			k8s.KubectlApply(t, kubeOpts, "../../manifests/backup-pvc.yaml")

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

			tests.WaitForDataPropagation()

			By("Verifying data before backup")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			_, value, err := prom.VectorScan(ctx, "backup_test_10")
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

			tests.WaitForDataPropagation()
			// Wait a bit for merge to complete
			time.Sleep(1 * time.Minute)

			By("Verifying data is downsampled")
			prom := tests.NewPromClientBuilder().
				ForVMSingle(namespace).
				WithStartTime(overwatch.Start).
				MustBuild()

			labels, value, err := prom.VectorScan(ctx, "count_over_time(downsample_test_0[5m])")
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
			_, value, err := prom.VectorScan(ctx, "retention_drop_0")
			require.EqualError(t, err, consts.ErrNoDataReturned)
			require.Equal(t, model.SampleValue(0), value)

			// Check kept data
			_, value, err = prom.VectorScan(ctx, "retention_keep_0")
			require.NoError(t, err)
			require.Equal(t, model.SampleValue(1), value)
		})
	})
})
