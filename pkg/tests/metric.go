package tests

import (
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
)

// ScannedMetric wraps a queried metric value with test context for rich assertion messages.
// The msg field is included in testify failure output so the failing query is always visible.
type ScannedMetric struct {
	t     require.TestingT
	value model.SampleValue
	msg   string
}

// NewScannedMetric creates a ScannedMetric with the given value and message string.
// msg is appended to assertion failures as the testify msgAndArgs parameter.
func NewScannedMetric(t require.TestingT, value model.SampleValue, msg string) ScannedMetric {
	return ScannedMetric{t: t, value: value, msg: msg}
}

func (s ScannedMetric) Greater(expected float64) {
	require.Greater(s.t, float64(s.value), expected, s.msg)
}

func (s ScannedMetric) GreaterOrEqual(expected float64) {
	require.GreaterOrEqual(s.t, float64(s.value), expected, s.msg)
}

func (s ScannedMetric) Less(expected float64) {
	require.Less(s.t, float64(s.value), expected, s.msg)
}

func (s ScannedMetric) EqualTo(expected model.SampleValue) {
	require.Equal(s.t, expected, s.value, s.msg)
}
