package tests

import (
	"context"
	"fmt"
	"time"

	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
)

// k6 testrun names are prefixed with the scenario name, so every query matches on the prefix.
func k6RunSelector(scenario string) string {
	return fmt.Sprintf(`testrun_name=~"^%s.*$"`, scenario)
}

// K6ReqsQuery counts completed k6 requests of a scenario kind ("insert"/"read").
// window is the rollup range, e.g. "15m" or "30m:" for a subquery.
func K6ReqsQuery(scenario, kind, window string) string {
	return fmt.Sprintf(`max_over_time(sum(k6_http_reqs_total{scenario=%q, %s})[%s])`, kind, k6RunSelector(scenario), window)
}

// K6FailureRateQuery returns the peak request failure rate, 0 when k6 reported none.
func K6FailureRateQuery(scenario, kind, window string) string {
	return fmt.Sprintf(`max(max_over_time(k6_http_req_failed_rate{scenario=%q, %s}[%s])) or vector(0)`, kind, k6RunSelector(scenario), window)
}

// K6P95Query returns the peak p95 request duration.
func K6P95Query(scenario, kind, window string) string {
	return fmt.Sprintf(`max(max_over_time(k6_http_req_duration_p95{scenario=%q, %s}[%s]))`, kind, k6RunSelector(scenario), window)
}

// K6DroppedIterationsQuery counts iterations k6 could not schedule, 0 when there were none.
func K6DroppedIterationsQuery(scenario, kind, window string) string {
	return fmt.Sprintf(`sum(max_over_time(k6_dropped_iterations_total{scenario=%q, %s}[%s])) or vector(0)`, kind, k6RunSelector(scenario), window)
}

// WaitForK6Metrics blocks until overwatch has scraped at least one k6 sample for the
// scenario and returns the timestamp to use as the end of the verification window.
func WaitForK6Metrics(ctx context.Context, t terratesting.TestingT, overwatch promquery.PrometheusClient, scenario string, start time.Time) time.Time {
	var end time.Time
	query := fmt.Sprintf(`sum(k6_http_reqs_total{%s})`, k6RunSelector(scenario))

	require.Eventually(t, func() bool {
		end = time.Now()
		values, _, err := overwatch.QueryRangeAt(ctx, query, start, end)
		if err != nil {
			return false
		}
		matrix, ok := values.(model.Matrix)
		if !ok {
			return false
		}
		for _, stream := range matrix {
			if len(stream.Values) > 0 {
				return true
			}
		}
		return false
	}, consts.PollingTimeout, consts.PollingInterval, "k6 metrics for %s were not scraped", scenario)

	return end
}
