package vl_functional_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestVLFunctionalTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.FlakeAttempts = 3
	RunSpecs(t, "VictoriaLogs Functional test Suite", suiteConfig, reporterConfig)
}

var (
	t         terratesting.TestingT
	namespace string
)

// Install VictoriaLogs stack for the first process, propagate t to the rest.
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()
		install.DiscoverIngressHost(ctx, t)

		// Stage 2 (parallel): vmgather + vm k8s stack + victorialogs single + collector (all need nginx host).
		tests.InstallVMStackAndGather(ctx, t)

		// Stage 3: install overwatch. Needs the VMAgent/VMAlert/VMSingle CRDs and the
		// "vmks" CRs installed by InstallVMK8StackWithHelm above.
		tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

// vlIngest posts JSONL log lines to a VictoriaLogs insert endpoint.
func vlIngest(ctx context.Context, insertURL, streamField, streamValue string, lines []string) error {
	var buf bytes.Buffer
	for _, line := range lines {
		buf.WriteString(line + "\n")
	}
	u := fmt.Sprintf("%s/insert/jsonline?_stream_fields=%s", insertURL, url.QueryEscape(streamField))
	return vlPost(ctx, u, "application/stream+json", buf.Bytes(), "ingest request")
}

func vlPost(ctx context.Context, targetURL, contentType string, payload []byte, op string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build %s: %w", op, err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s returned %s: %s", op, resp.Status, string(body))
	}
	return nil
}

// vlQuery runs a LogsQL query and returns the response body.
func vlQuery(ctx context.Context, selectURL, query string, start, end time.Time) ([]byte, error) {
	u, err := url.Parse(selectURL + "/select/logsql/query")
	if err != nil {
		return nil, fmt.Errorf("build query url: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("start", start.UTC().Format(time.RFC3339))
	q.Set("end", end.UTC().Format(time.RFC3339))
	u.RawQuery = q.Encode()
	return vlGet(ctx, u.String(), "query request")
}

// vlStatsCount calls /select/logsql/stats_query and returns the raw body.
func vlStatsCount(ctx context.Context, selectURL, query string) ([]byte, error) {
	u, err := url.Parse(selectURL + "/select/logsql/stats_query")
	if err != nil {
		return nil, fmt.Errorf("build stats url: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()
	return vlGet(ctx, u.String(), "stats request")
}

func vlGet(ctx context.Context, targetURL, op string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", op, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", op, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("%s returned %s: %s", op, resp.Status, string(body))
	}
	return body, nil
}

// installVLSingle installs victoria-logs-single in the given namespace and returns its ingress URL.
func installVLSingle(ctx context.Context, ns, releaseName string) string {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLSingleChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}
	opts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.VictoriaLogsSingleValuesFile()},
		SetValues: map[string]string{
			"server.ingress.enabled":          "true",
			"server.ingress.ingressClassName": "nginx",
			"server.ingress.hosts[0].name":    consts.VLNamespacedHost(ns),
			"server.ingress.hosts[0].path[0]": "/",
		},
		ExtraArgs: map[string][]string{"upgrade": upgradeArgs},
	}
	if v := consts.VLVersion(); v != "" {
		opts.SetValues["server.image.tag"] = v
	}
	By(fmt.Sprintf("Install %s as %s in %s", consts.VictoriaLogsSingleChart, releaseName, ns))
	if err := helm.UpgradeE(t, opts, consts.VictoriaLogsSingleChart, releaseName); err != nil {
		t.Fatalf("failed to install %s: %v", consts.VictoriaLogsSingleChart, err)
	}
	vlURL := consts.VLUrl(ns)
	helpers.WaitForHTTPRoute(ctx, t, vlURL+"/health")
	return vlURL
}

// uninstallRelease removes a Helm release from the namespace.
func uninstallRelease(ns, releaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	opts := &helm.Options{KubectlOptions: kubeOpts}
	By(fmt.Sprintf("Uninstall %s from %s", releaseName, ns))
	_ = helm.DeleteE(t, opts, releaseName, true)
}

// installVLCollector installs victoria-logs-collector pointed at the given VLSingle insert URL.
func installVLCollector(ctx context.Context, ns, releaseName, vlSingleAddr string) {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLCollectorChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}
	opts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.VictoriaLogsCollectorValuesFile()},
		SetValues:      map[string]string{"remoteWrite[0].url": fmt.Sprintf("http://%s", vlSingleAddr)},
		ExtraArgs:      map[string][]string{"upgrade": upgradeArgs},
	}
	By(fmt.Sprintf("Install %s as %s in %s", consts.VictoriaLogsCollectorChart, releaseName, ns))
	if err := helm.UpgradeE(t, opts, consts.VictoriaLogsCollectorChart, releaseName); err != nil {
		t.Fatalf("failed to install %s: %v", consts.VictoriaLogsCollectorChart, err)
	}
}

var _ = Describe("VLSingle", Label("vlsingle"), func() {
	const releaseName = "vl-single-test"
	var vlURL string
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		namespace = tests.RandomNamespace("vl")
		vlURL = installVLSingle(ctx, namespace, releaseName)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		uninstallRelease(namespace, releaseName)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should ingest and query logs back",
		Label("id=e40691aa-e79e-41d1-9397-7585aaac4f9a"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"hello vlsingle","test_id":%q}`,
				ingestTime.Format(time.RFC3339Nano), testLabel)

			Expect(vlIngest(ctx, vlURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, vlURL, fmt.Sprintf(`{test_id=%q}`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello vlsingle"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=338d24c1-7639-42e6-b9a1-9d99d6dac6d3"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)
			now := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"stats test","test_id":%q}`,
				now.Format(time.RFC3339Nano), testLabel)

			Expect(vlIngest(ctx, vlURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlStatsCount(ctx, vlURL, `* | stats count() total`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("total"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return 200 for a wildcard query",
		Label("id=f3a6ea21-081e-4f72-84c8-80fbca451cf0"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			Eventually(func(g Gomega) {
				_, err := vlQuery(ctx, vlURL, "*",
					time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should ingest Elasticsearch bulk logs",
		Label("id=6d4f6357-92d6-4db5-a42c-f8f3c02f8668"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-es-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"create":{}}
{"_time":%q,"_msg":"hello elasticsearch bulk","test_id":%q}
`, ingestTime.Format(time.RFC3339Nano), testLabel)

			Expect(vlPost(ctx, vlURL+"/insert/elasticsearch/_bulk", "application/json", []byte(payload), "elasticsearch bulk ingest")).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, vlURL, fmt.Sprintf(`test_id:%q`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello elasticsearch bulk"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should ingest Loki push logs",
		Label("id=cfdd5ac7-7080-4974-8fdd-0f6e6f6a8263"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-loki-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"streams":[{"stream":{"test_id":%q,"job":"e2e"},"values":[[%q,"hello loki push"]]}]}`,
				testLabel, fmt.Sprintf("%d", ingestTime.UnixNano()))

			Expect(vlPost(ctx, vlURL+"/insert/loki/api/v1/push", "application/json", []byte(payload), "loki push ingest")).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, vlURL, fmt.Sprintf(`{test_id=%q} "hello loki push"`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello loki push"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should ingest OTLP logs",
		Label("id=f1692c65-93b0-420d-88b9-952b38c35012"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-otlp-%s", namespace)
			ingestTime := time.Now().UTC()

			payload, err := proto.Marshal(&collogspb.ExportLogsServiceRequest{
				ResourceLogs: []*logspb.ResourceLogs{
					{
						ScopeLogs: []*logspb.ScopeLogs{
							{
								LogRecords: []*logspb.LogRecord{
									{
										TimeUnixNano: uint64(ingestTime.UnixNano()),
										Body: &commonpb.AnyValue{
											Value: &commonpb.AnyValue_StringValue{
												StringValue: "hello otlp logs",
											},
										},
										Attributes: []*commonpb.KeyValue{
											{
												Key: "test_id",
												Value: &commonpb.AnyValue{
													Value: &commonpb.AnyValue_StringValue{
														StringValue: testLabel,
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
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(vlPost(ctx, vlURL+"/insert/opentelemetry/v1/logs", "application/x-protobuf", payload, "otlp logs ingest")).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, vlURL, fmt.Sprintf(`test_id:%q`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello otlp logs"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})
})

var _ = Describe("VLCluster", Label("vlcluster"), func() {
	var insertURL, selectURL string
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		namespace = tests.RandomNamespace("vlc")
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		vlclient := install.GetVMClient(t, kubeOpts)
		namePatch := tests.NewJSONPatchBuilder().Add("/metadata/name", namespace).MustBuild()
		install.InstallVLCluster(ctx, t, kubeOpts, namespace, vlclient, []jsonpatch.Patch{namePatch}, consts.VMClusterWaitTimeout)
		insertURL = consts.VLInsertUrl(namespace)
		selectURL = consts.VLSelectUrl(namespace)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		install.DeleteVLCluster(t, kubeOpts, namespace)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should ingest and query logs back",
		Label("id=8cfb59a7-cf4c-44f2-8951-6b6edd050add"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"hello vlcluster","test_id":%q}`,
				ingestTime.Format(time.RFC3339Nano), testLabel)

			Expect(vlIngest(ctx, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, selectURL, fmt.Sprintf(`{test_id=%q}`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello vlcluster"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=5e9f0c3c-bf44-4179-af74-aa96e5ea1e2c"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)
			now := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"stats test","test_id":%q}`,
				now.Format(time.RFC3339Nano), testLabel)

			Expect(vlIngest(ctx, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlStatsCount(ctx, selectURL, `* | stats count() total`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("total"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return 200 for a wildcard query",
		Label("id=5a2b45e9-5c58-4a09-a6b2-1aacb8bbe2f7"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			Eventually(func(g Gomega) {
				_, err := vlQuery(ctx, selectURL, "*",
					time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

})

var _ = Describe("VLCollector", Label("vlcollector"), func() {
	const (
		singleReleaseName    = "vl-col-single-test"
		collectorReleaseName = "vl-collector-test"
	)
	var vlURL string
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		namespace = tests.RandomNamespace("vlcol")
		vlURL = installVLSingle(ctx, namespace, singleReleaseName)
		installVLCollector(ctx, namespace, collectorReleaseName, consts.GetVLSingleSvc(singleReleaseName, namespace))
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		uninstallRelease(namespace, collectorReleaseName)
		uninstallRelease(namespace, singleReleaseName)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should collect pod logs and forward them to VLSingle",
		Label("id=d80de1a3-3ead-404f-8a53-536b7292d9ce"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			// Deploy a pod that emits a unique log line.
			testLabel := fmt.Sprintf("e2e-col-%s", namespace)
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			podName := "log-emitter"
			manifest, err := os.ReadFile(consts.LogEmitterYaml())
			Expect(err).NotTo(HaveOccurred())
			podYAML := strings.NewReplacer(
				"log-emitter-namespace", namespace,
				"log-emitter-message", testLabel,
			).Replace(string(manifest))

			By("Deploy log-emitter pod")
			install.KubectlApplyFromString(ctx, t, kubeOpts, podYAML)
			k8s.WaitUntilPodAvailable(t, kubeOpts, podName, 30, 5*time.Second)

			By("Wait for collector to ship the log line to VLSingle")
			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, vlURL,
					fmt.Sprintf(`{kubernetes.pod_name=%q} %q`, podName, testLabel),
					time.Now().Add(-5*time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring(testLabel))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})
})
