package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:stylecheck,staticcheck

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
)

// NewMetricChecker returns a closure that queries overwatch for query, asserts the result
// is a non-empty matrix, and wraps the last sample value in a ScannedMetric for assertions.
//
// If start and end are both zero, the query is evaluated at "now" via QueryRange; otherwise
// it is evaluated over [start, end] via QueryRangeAt.
func NewMetricChecker(t require.TestingT, ctx context.Context, overwatch promquery.PrometheusClient, start, end time.Time) func(purpose, query string) ScannedMetric {
	return func(purpose, query string) ScannedMetric {
		By(purpose)
		timestamp := time.Now().Format(time.RFC3339)

		var values model.Value
		var err error
		if start.IsZero() && end.IsZero() {
			values, _, err = overwatch.QueryRange(ctx, query)
		} else {
			values, _, err = overwatch.QueryRangeAt(ctx, query, start, end)
		}
		require.NoError(t, err, "Failed to make a query %q at time %s", purpose, timestamp)

		matrix, ok := values.(model.Matrix)
		require.True(t, ok, "query %q returned %s instead of matrix", purpose, values.Type())
		require.NotEmpty(t, matrix, "query %q returned no series", purpose)
		samples := matrix[0].Values
		require.NotEmpty(t, samples, "query %q returned no samples", purpose)
		lastValue := samples[len(samples)-1].Value

		params := []MetricParameter{
			{Name: "query", Value: query},
			{Name: "timestamp", Value: timestamp},
		}
		if !start.IsZero() {
			params = append(params, MetricParameter{Name: "start", Value: start.Format(time.RFC3339)})
		}
		if !end.IsZero() {
			params = append(params, MetricParameter{Name: "end", Value: end.Format(time.RFC3339)})
		}
		params = append(params, MetricParameter{Name: "value", Value: fmt.Sprintf("%v", lastValue)})

		return NewScannedMetric(t, lastValue, purpose, params...)
	}
}
