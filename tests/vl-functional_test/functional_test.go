package vl_functional_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

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
	vlBaseURL string
)

// Install VictoriaLogs for the first process, set namespace for the rest
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()
		install.DiscoverIngressHost(ctx, t)

		install.InstallVMGather(ctx, t)
		install.InstallVictoriaLogs(ctx, t, consts.DefaultVMNamespace, consts.DefaultVLReleaseName, consts.DefaultVLCollectorReleaseName)
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = BeforeEach(func(ctx context.Context) {
	namespace = tests.RandomNamespace("vl")
	vlBaseURL = fmt.Sprintf("http://%s", consts.VLHost())
})

// vlQuery executes a LogsQL query against VictoriaLogs and returns the response body.
func vlQuery(ctx context.Context, query string) ([]byte, int, error) {
	u := url.URL{
		Scheme: "http",
		Host:   consts.VLHost(),
		Path:   "/select/logsql/query",
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("start", time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	q.Set("end", time.Now().UTC().Format(time.RFC3339))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return body, resp.StatusCode, nil
}

var _ = Describe("VictoriaLogs functional test", Label("victorialogs"), func() {
	Describe("Query API", Label("query"), func() {
		It("should return 200 for a wildcard query",
			Label("id=a1b2c3d4-0001-0001-0001-000000000001"),
			func(ctx context.Context) {
				Eventually(func(g Gomega) {
					_, status, err := vlQuery(ctx, "*")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(status).To(Equal(http.StatusOK))
				}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
			})

		It("should return logs matching a stream selector",
			Label("id=a1b2c3d4-0001-0001-0001-000000000002"),
			func(ctx context.Context) {
				Eventually(func(g Gomega) {
					body, status, err := vlQuery(ctx, `{namespace!=""}`)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(status).To(Equal(http.StatusOK))
					// Response is JSONL — at least one log line expected after collector is running
					g.Expect(len(body)).To(BeNumerically(">", 0))
				}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
			})
	})

	Describe("Stats API", Label("stats"), func() {
		It("should return HTTP 200 from /select/logsql/stats_query",
			Label("id=a1b2c3d4-0001-0001-0002-000000000001"),
			func(ctx context.Context) {
				u := url.URL{
					Scheme: "http",
					Host:   consts.VLHost(),
					Path:   "/select/logsql/stats_query",
				}
				q := u.Query()
				q.Set("query", `* | stats count() total`)
				u.RawQuery = q.Encode()

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
				Expect(err).NotTo(HaveOccurred())

				Eventually(func(g Gomega) {
					resp, err := http.DefaultClient.Do(req.Clone(ctx))
					g.Expect(err).NotTo(HaveOccurred())
					defer resp.Body.Close()
					g.Expect(resp.StatusCode).To(Equal(http.StatusOK))
				}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
			})
	})

	Describe("Ingestion", Label("ingestion"), func() {
		It("should ingest logs via HTTP and query them back",
			Label("id=a1b2c3d4-0001-0001-0003-000000000001"),
			func(ctx context.Context) {
				// Use unique stream label so we can filter precisely
				streamLabel := fmt.Sprintf("e2e-test-%s", namespace)
				payload := fmt.Sprintf(
					`{"_time":%q,"_msg":"hello from e2e test","test_ns":%q}`,
					time.Now().UTC().Format(time.RFC3339Nano),
					streamLabel,
				)

				ingestURL := fmt.Sprintf("%s/insert/jsonline?_stream_fields=test_ns", vlBaseURL)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, ingestURL,
					bytes.NewBufferString(payload),
				)
				Expect(err).NotTo(HaveOccurred())
				req.Header.Set("Content-Type", "application/stream+json")

				resp, err := http.DefaultClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				// Query back
				Eventually(func(g Gomega) {
					body, status, err := vlQuery(ctx, fmt.Sprintf(`{test_ns=%q}`, streamLabel))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(status).To(Equal(http.StatusOK))
					g.Expect(string(body)).To(ContainSubstring("hello from e2e test"))
				}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
			})
	})
})
