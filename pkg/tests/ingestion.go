package tests

import (
	"context"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2" //nolint:stylecheck,staticcheck

	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
)

// VerifyIngestedMetric queries prom for metricName, asserts the last sample equals
// expectedValue, and asserts every entry in expectedLabels is present on the result with
// the expected value. Callers build prom themselves (its construction differs between
// VMSingle/VMCluster/tenant-scoped clients) and are expected to have already ingested the
// metric via whichever protocol (InfluxDB/Datadog/OpenTelemetry/...) they're testing.
func VerifyIngestedMetric(ctx context.Context, t terratesting.TestingT, namespace string, prom promquery.PrometheusClient, metricName string, expectedValue model.SampleValue, expectedLabels map[string]model.LabelValue) {
	By("Verifying data via Prometheus protocol")
	labels, value, err := RetryVectorScan(ctx, t, namespace, prom, metricName, 5)
	require.NoError(t, err)
	NewScannedMetric(t, value, metricName).EqualTo(expectedValue)
	for name, expected := range expectedLabels {
		require.Equal(t, expected, labels[model.LabelName(name)])
	}
}
