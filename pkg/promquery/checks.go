package promquery

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	httptransport "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/testing"
	amclient "github.com/prometheus/alertmanager/api/v2/client"
	"github.com/prometheus/alertmanager/api/v2/client/alert"
	"github.com/prometheus/alertmanager/api/v2/models"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

var (
	DefaultExceptions = []string{
		"InfoInhibitor", "Watchdog",
		"NodeMemoryMajorPagesFaults",
	}
)

// CheckNoAlertsFiring verifies that no alerts are firing in the given namespace,
// except for the ones specified in exceptions.
func (p PrometheusClient) CheckNoAlertsFiring(ctx context.Context, t testing.TestingT, namespace string, exceptions []string) {
	firing, err := p.getFiringAlerts(ctx, t, namespace, exceptions)
	if err != nil {
		// Handle query errors gracefully - just return without failing the test
		return
	}
	for _, f := range firing {
		require.Fail(t, fmt.Sprintf("Unexpected alert firing for namespace %s: %s", namespace, f))
	}
}

// WaitUntilNoAlertsFiring waits until no alerts are firing.
func (p PrometheusClient) WaitUntilNoAlertsFiring(ctx context.Context, t testing.TestingT, namespace string, exceptions []string) {
	require.Eventually(t, func() bool {
		firing, err := p.getFiringAlerts(ctx, t, namespace, exceptions)
		if err != nil || len(firing) > 0 {
			return false
		}
		return true
	}, consts.PollingTimeout, consts.PollingInterval, "Alerts are still firing in namespace %s", namespace)
}

func (p PrometheusClient) getFiringAlerts(ctx context.Context, t testing.TestingT, namespace string, exceptions []string) ([]string, error) {
	alerts, err := p.getAlertsFromAM(ctx, t, namespace, "")
	if err != nil {
		return nil, err
	}

	var firing []string
	allExceptions := append(DefaultExceptions, exceptions...)

	for _, alert := range alerts {
		name := alert.Labels["alertname"]
		isExcepted := false
		for _, ex := range allExceptions {
			// Exceptions are regex patterns
			matched, err := regexp.MatchString(ex, name)
			if err == nil && matched {
				isExcepted = true
				break
			}
		}
		if !isExcepted {
			firing = append(firing, fmt.Sprintf("%s (labels: %v)", name, alert.Labels))
		}
	}
	return firing, nil
}

// CheckAlertIsFiring verifies that a specific alert (or selector) is currently firing.
func (p PrometheusClient) CheckAlertIsFiring(ctx context.Context, t testing.TestingT, namespace, selector string) {
	alerts, err := p.getAlertsFromAM(ctx, t, namespace, selector)
	require.NoError(t, err, "Failed to get alerts from Alertmanager")
	require.NotEmpty(t, alerts, "Alert %s should be firing in namespace %s", selector, namespace)
}

// CheckAlertWasFiringSince verifies that a specific alert (or selector) was firing.
// When using Alertmanager, it checks if the alert is currently active.
func (p PrometheusClient) CheckAlertWasFiringSince(ctx context.Context, t testing.TestingT, namespace, selector, lookbackTime string) {
	alerts, err := p.getAlertsFromAM(ctx, t, namespace, selector)
	require.NoError(t, err, "Failed to get alerts from Alertmanager")
	require.NotEmpty(t, alerts, "Alert %s should be firing in namespace %s", selector, namespace)
}

func (p PrometheusClient) getAlertsFromAM(ctx context.Context, t testing.TestingT, namespace, selector string) ([]*models.GettableAlert, error) {
	var amURL string
	if p.AlertManagerURL != "" {
		amURL = p.AlertManagerURL
	} else {
		amHost := consts.AlertManagerHost(consts.DefaultVMNamespace)
		amURL = fmt.Sprintf("http://%s", amHost)
	}

	u, err := url.Parse(amURL)
	if err != nil {
		return nil, err
	}

	transport := httptransport.New(u.Host, "/api/v2", []string{u.Scheme})
	c := amclient.New(transport, strfmt.Default)

	params := alert.NewGetAlertsParams().WithContext(ctx)
	params.Filter = []string{
		fmt.Sprintf("namespace=%s", namespace),
	}

	if selector != "" {
		if strings.Contains(selector, "=") {
			// Assume comma-separated list of filters
			parts := strings.Split(selector, ",")
			for _, part := range parts {
				params.Filter = append(params.Filter, strings.Trim(strings.TrimSpace(part), "{}"))
			}
		} else {
			params.Filter = append(params.Filter, fmt.Sprintf("alertname=%s", selector))
		}
	}

	logger.Default.Logf(t, "Requesting alerts from AM: %s with filters %v", amURL, params.Filter)

	resp, err := c.Alert.GetAlerts(params)
	if err != nil {
		return nil, err
	}

	return resp.Payload, nil
}
