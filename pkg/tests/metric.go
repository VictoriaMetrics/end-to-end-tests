package tests

import (
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests/allure"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
)

// ScannedMetric wraps a queried metric value with test context for rich assertion messages.
// Parameters are added to the Allure result only when the assertion fails.
type ScannedMetric struct {
	t          require.TestingT
	value      model.SampleValue
	msg        string
	parameters []MetricParameter
}

type MetricParameter struct {
	Name  string
	Value string
}

// NewScannedMetric creates a ScannedMetric with the given value and message string.
// msg is appended to assertion failures as the testify msgAndArgs parameter.
func NewScannedMetric(t require.TestingT, value model.SampleValue, msg string, parameters ...MetricParameter) ScannedMetric {
	return ScannedMetric{t: t, value: value, msg: msg, parameters: parameters}
}

func (s ScannedMetric) Greater(expected float64) {
	if !(float64(s.value) > expected) {
		s.addFailureParameters()
	}
	require.Greater(s.t, float64(s.value), expected, s.msg)
}

func (s ScannedMetric) GreaterOrEqual(expected float64) {
	if !(float64(s.value) >= expected) {
		s.addFailureParameters()
	}
	require.GreaterOrEqual(s.t, float64(s.value), expected, s.msg)
}

func (s ScannedMetric) Less(expected float64) {
	if !(float64(s.value) < expected) {
		s.addFailureParameters()
	}
	require.Less(s.t, float64(s.value), expected, s.msg)
}

func (s ScannedMetric) EqualTo(expected model.SampleValue) {
	if s.value != expected {
		s.addFailureParameters()
	}
	require.Equal(s.t, expected, s.value, s.msg)
}

func (s ScannedMetric) addFailureParameters() {
	for _, p := range s.parameters {
		allure.AddParameter(p.Name, p.Value)
	}
}
