package install

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
)

func waitForHTTPRoute(ctx context.Context, t terratesting.TestingT, readyURL string) {
	waitForHTTPRoutes(ctx, t, readyURL)
}

func waitForHTTPRoutes(ctx context.Context, t terratesting.TestingT, readyURLs ...string) {
	client := &http.Client{
		Timeout: consts.HTTPClientTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Test readiness probe accepts self-signed ingress/backend certs.
		},
	}
	require.Eventually(t, func() bool {
		for _, readyURL := range readyURLs {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
			if err != nil {
				continue
			}

			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		return false
	}, consts.ResourceWaitTimeout, consts.PollingInterval, "ingress routes %s did not become ready", strings.Join(readyURLs, ", "))
}
