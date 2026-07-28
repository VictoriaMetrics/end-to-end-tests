package vl_functional_test

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestVLFunctionalTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
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

		// Stage 2 (parallel): vmgather + victorialogs single + collector (both need nginx host).
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMGather(ctx, t)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVictoriaLogs(ctx, t, consts.DefaultVMNamespace, consts.DefaultVLReleaseName, consts.DefaultVLCollectorReleaseName)
		}()
		wg.Wait()

		// Stage 3: install overwatch.
		install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

// runVLRequest executes HTTP requests from inside the namespace, where Kubernetes service DNS works.
func runVLRequest(ctx context.Context, ns, method, targetURL string, payload []byte) ([]byte, error) {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	args := []string{
		"run", fmt.Sprintf("vl-curl-%d", time.Now().UnixNano()),
		"--rm", "-i", "--restart=Never",
		"--image=curlimages/curl:8.15.0",
		"--command", "--",
		"curl", "-sS", "-X", method, "--fail-with-body",
	}
	if payload != nil {
		args = append(args, "-H", "Content-Type: application/stream+json", "--data-binary", string(payload))
	}
	args = append(args, targetURL)

	out, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts, args...)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// vlIngest posts JSONL log lines to a VictoriaLogs insert endpoint.
func vlIngest(ctx context.Context, ns, insertURL, streamField, streamValue string, lines []string) error {
	var buf bytes.Buffer
	for _, line := range lines {
		buf.WriteString(line + "\n")
	}
	u := fmt.Sprintf("%s/insert/jsonline?_stream_fields=%s", insertURL, streamField)
	_, err := runVLRequest(ctx, ns, "POST", u, buf.Bytes())
	if err != nil {
		return fmt.Errorf("ingest request: %w", err)
	}
	return nil
}

// vlQuery runs a LogsQL query and returns the response body.
func vlQuery(ctx context.Context, ns, selectURL, query string, start, end time.Time) ([]byte, error) {
	u := fmt.Sprintf("http://%s/select/logsql/query?query=%s&start=%s&end=%s",
		selectURL,
		urlQueryEscape(query),
		urlQueryEscape(start.UTC().Format(time.RFC3339)),
		urlQueryEscape(end.UTC().Format(time.RFC3339)),
	)
	body, err := runVLRequest(ctx, ns, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("query request: %w", err)
	}
	return body, nil
}

// vlStatsCount calls /select/logsql/stats_query and returns the raw body.
func vlStatsCount(ctx context.Context, ns, selectURL, query string) ([]byte, error) {
	u := fmt.Sprintf("http://%s/select/logsql/stats_query?query=%s", selectURL, urlQueryEscape(query))
	body, err := runVLRequest(ctx, ns, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("stats request: %w", err)
	}
	return body, nil
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// installVLSingle installs victoria-logs-single in the given namespace and returns its in-cluster svc address.
func installVLSingle(ctx context.Context, ns, releaseName string) string {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLSingleChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}
	opts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.VictoriaLogsSingleValuesFile()},
		ExtraArgs:      map[string][]string{"upgrade": upgradeArgs},
	}
	By(fmt.Sprintf("Install %s as %s in %s", consts.VictoriaLogsSingleChart, releaseName, ns))
	if err := helm.UpgradeE(t, opts, consts.VictoriaLogsSingleChart, releaseName); err != nil {
		t.Fatalf("failed to install %s: %v", consts.VictoriaLogsSingleChart, err)
	}
	return consts.GetVLSingleSvc(releaseName, ns)
}

// installVLCluster installs victoria-logs-cluster and returns (insertSvc, selectSvc).
func installVLCluster(ctx context.Context, ns, releaseName string) (string, string) {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLClusterChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}
	opts := &helm.Options{
		KubectlOptions: kubeOpts,
		ExtraArgs:      map[string][]string{"upgrade": upgradeArgs},
	}
	By(fmt.Sprintf("Install %s as %s in %s", consts.VictoriaLogsClusterChart, releaseName, ns))
	if err := helm.UpgradeE(t, opts, consts.VictoriaLogsClusterChart, releaseName); err != nil {
		t.Fatalf("failed to install %s: %v", consts.VictoriaLogsClusterChart, err)
	}
	insertSvc := fmt.Sprintf("%s-victoria-logs-cluster-vlinsert.%s.svc.cluster.local:9481", releaseName, ns)
	selectSvc := fmt.Sprintf("%s-victoria-logs-cluster-vlselect.%s.svc.cluster.local:9471", releaseName, ns)
	return insertSvc, selectSvc
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
	var svcAddr string

	BeforeEach(func(ctx context.Context) {
		namespace = tests.RandomNamespace("vl")
		svcAddr = installVLSingle(ctx, namespace, releaseName)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailure(ctx, t, kubeOpts, namespace)
		uninstallRelease(namespace, releaseName)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should ingest and query logs back",
		Label("id=e40691aa-e79e-41d1-9397-7585aaac4f9a"),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"hello vlsingle","test_id":%q}`,
				ingestTime.Format(time.RFC3339Nano), testLabel)

			insertURL := fmt.Sprintf("http://%s", svcAddr)
			Expect(vlIngest(ctx, namespace, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, namespace, svcAddr, fmt.Sprintf(`{test_id=%q}`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello vlsingle"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=338d24c1-7639-42e6-b9a1-9d99d6dac6d3"),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)
			now := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"stats test","test_id":%q}`,
				now.Format(time.RFC3339Nano), testLabel)

			insertURL := fmt.Sprintf("http://%s", svcAddr)
			Expect(vlIngest(ctx, namespace, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlStatsCount(ctx, namespace, svcAddr, `* | stats count() total`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("total"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return 200 for a wildcard query",
		Label("id=f3a6ea21-081e-4f72-84c8-80fbca451cf0"),
		func(ctx context.Context) {
			Eventually(func(g Gomega) {
				_, err := vlQuery(ctx, namespace, svcAddr, "*",
					time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})
})

var _ = Describe("VLCluster", Label("vlcluster"), func() {
	const releaseName = "vl-cluster-test"
	var insertSvc, selectSvc string

	BeforeEach(func(ctx context.Context) {
		namespace = tests.RandomNamespace("vlc")
		insertSvc, selectSvc = installVLCluster(ctx, namespace, releaseName)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailure(ctx, t, kubeOpts, namespace)
		uninstallRelease(namespace, releaseName)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should ingest and query logs back",
		Label("id=8cfb59a7-cf4c-44f2-8951-6b6edd050add"),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"hello vlcluster","test_id":%q}`,
				ingestTime.Format(time.RFC3339Nano), testLabel)

			insertURL := fmt.Sprintf("http://%s", insertSvc)
			Expect(vlIngest(ctx, namespace, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, namespace, selectSvc, fmt.Sprintf(`{test_id=%q}`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello vlcluster"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=5e9f0c3c-bf44-4179-af74-aa96e5ea1e2c"),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)
			now := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"stats test","test_id":%q}`,
				now.Format(time.RFC3339Nano), testLabel)

			insertURL := fmt.Sprintf("http://%s", insertSvc)
			Expect(vlIngest(ctx, namespace, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := vlStatsCount(ctx, namespace, selectSvc, `* | stats count() total`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("total"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return 200 for a wildcard query",
		Label("id=5a2b45e9-5c58-4a09-a6b2-1aacb8bbe2f7"),
		func(ctx context.Context) {
			Eventually(func(g Gomega) {
				_, err := vlQuery(ctx, namespace, selectSvc, "*",
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
	var svcAddr string

	BeforeEach(func(ctx context.Context) {
		namespace = tests.RandomNamespace("vlcol")
		svcAddr = installVLSingle(ctx, namespace, singleReleaseName)
		installVLCollector(ctx, namespace, collectorReleaseName, svcAddr)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailure(ctx, t, kubeOpts, namespace)
		uninstallRelease(namespace, collectorReleaseName)
		uninstallRelease(namespace, singleReleaseName)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should collect pod logs and forward them to VLSingle",
		Label("id=d80de1a3-3ead-404f-8a53-536b7292d9ce"),
		func(ctx context.Context) {
			// Deploy a pod that emits a unique log line.
			testLabel := fmt.Sprintf("e2e-col-%s", namespace)
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			podName := "log-emitter"
			podYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: log-emitter
spec:
  restartPolicy: Never
  containers:
  - name: emitter
    image: busybox
    command: ["sh", "-c", "echo %s; sleep 3600"]
`, podName, namespace, testLabel)

			By("Deploy log-emitter pod")
			install.KubectlApplyFromString(ctx, t, kubeOpts, podYAML)
			k8s.WaitUntilPodAvailable(t, kubeOpts, podName, 30, 5*time.Second)

			By("Wait for collector to ship the log line to VLSingle")
			Eventually(func(g Gomega) {
				body, err := vlQuery(ctx, namespace, svcAddr,
					fmt.Sprintf(`{pod=%q} %q`, podName, testLabel),
					time.Now().Add(-5*time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring(testLabel))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})
})
