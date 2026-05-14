package install

import (
	"context"
	"net/http"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
)

func waitForHTTPRoute(ctx context.Context, t terratesting.TestingT, readyURL string) {
	client := &http.Client{Timeout: consts.HTTPClientTimeout}
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		if err != nil {
			return false
		}

		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		return resp.StatusCode == http.StatusOK
	}, consts.ResourceWaitTimeout, consts.PollingInterval, "ingress route %s did not become ready", readyURL)
}
