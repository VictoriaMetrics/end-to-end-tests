package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestK6RunSelector(t *testing.T) {
	require.Equal(t, `testrun_name="baseline-abcxyz"`, k6RunSelector("baseline-abcxyz"))
	require.Equal(t, `testrun_name="baseline-defuvw"`, k6RunSelector("baseline-defuvw"))
}

func TestK6FailureRateQuery(t *testing.T) {
	got := K6FailureRateQuery("queue-overflow", "insert", "15m")
	want := `100 * sum(max_over_time(k6_http_reqs_total{scenario="insert", testrun_name="queue-overflow", expected_response="false"}[15m])) / sum(max_over_time(k6_http_reqs_total{scenario="insert", testrun_name="queue-overflow"}[15m])) or vector(0)`
	require.Equal(t, want, got)
}
