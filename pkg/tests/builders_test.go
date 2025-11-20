package tests

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigMapBuilder(t *testing.T) {
	name := "test-configmap"
	relabelConfig := "relabel_configs:\n  - action: keep"
	streamAggrConfig := "stream_aggregation:\n  - match: http_requests_total"

	builder := NewConfigMapBuilder(name).
		WithRelabelConfig(relabelConfig).
		WithStreamAggrConfig(streamAggrConfig)

	cm := builder.build()

	assert.Equal(t, name, cm.Name)
	assert.Equal(t, "v1", cm.APIVersion)
	assert.Equal(t, "ConfigMap", cm.Kind)
	assert.Equal(t, relabelConfig, cm.Data["relabel.yml"])
	assert.Equal(t, streamAggrConfig, cm.Data["stream-aggr.yml"])
}

func TestJSONPatchBuilder(t *testing.T) {
	t.Run("WithExtraArg", func(t *testing.T) {
		builder := NewJSONPatchBuilder().
			WithExtraArg("foo", "bar")

		patch := builder.MustBuild()
		assert.NotNil(t, patch)
	})

	t.Run("WithVMSingleConfig", func(t *testing.T) {
		builder := NewJSONPatchBuilder().
			WithVMSingleConfig("my-cm", "config-key", "file.yml")

		patch := builder.MustBuild()
		assert.NotNil(t, patch)
	})
}

func TestPromClientBuilder(t *testing.T) {
	t.Run("WithBaseURL", func(t *testing.T) {
		url := "http://example.com"
		builder := NewPromClientBuilder().
			WithBaseURL(url)

		client, err := builder.build()
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("WithStartTime", func(t *testing.T) {
		now := time.Now()
		builder := NewPromClientBuilder().
			WithBaseURL("http://example.com").
			WithStartTime(now)

		client, err := builder.build()
		require.NoError(t, err)
		assert.Equal(t, now, client.Start)
	})

	t.Run("Build Error", func(t *testing.T) {
		builder := NewPromClientBuilder()
		_, err := builder.build()
		assert.Error(t, err)
		assert.Equal(t, "no URL configured for Prometheus client", err.Error())
	})
}

func TestTimeSeriesBuilder(t *testing.T) {
	prefix := "test_metric"
	count := 5
	val := 123.45

	ts := NewTimeSeriesBuilder(prefix).
		WithCount(count).
		WithValue(val).
		WithLabel("env", "prod").
		WithTenantLabel(100).
		Build()

	require.Len(t, ts, count)

	for _, series := range ts {
		labels := make(map[string]string)
		for _, l := range series.Labels {
			labels[l.Name] = l.Value
		}

		assert.Equal(t, "prod", labels["env"])
		assert.Equal(t, "100", labels["vm_account_id"])
		assert.Contains(t, labels["__name__"], prefix)

		require.Len(t, series.Samples, 1)
		assert.Equal(t, val, series.Samples[0].Value)
	}
}

func TestRemoteWriteBuilder(t *testing.T) {
	t.Run("WithURL", func(t *testing.T) {
		url := "http://rw.example.com"
		builder := NewRemoteWriteBuilder().WithURL(url)
		assert.Equal(t, url, builder.url)
	})

	t.Run("Send Error", func(t *testing.T) {
		builder := NewRemoteWriteBuilder()
		err := builder.Send(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no URL configured")
	})
}

func TestRelabelConfigBuilder(t *testing.T) {
	config := NewRelabelConfigBuilder().
		AddLabel("target", "replacement").
		DropByName("drop_regex").
		MustBuild()

	assert.Contains(t, config, "- replacement: replacement")
	assert.Contains(t, config, "target_label: target")
	assert.Contains(t, config, "action: drop")
	assert.Contains(t, config, "source_labels:")
	assert.Contains(t, config, "- __name__")
	assert.Contains(t, config, "regex: drop_regex")
}

func TestStreamAggrConfigBuilder(t *testing.T) {
	config := NewStreamAggrConfigBuilder().
		AddRule("match_regex", "1m", []string{"sum_samples", "count_samples"}).
		WithoutLabels("pod", "instance").
		MustBuild()

	assert.Contains(t, config, "match: match_regex")
	assert.Contains(t, config, "interval: 1m")
	assert.Contains(t, config, "outputs:")
	assert.Contains(t, config, "- sum_samples")
	assert.Contains(t, config, "- count_samples")
	assert.Contains(t, config, "without:")
	assert.Contains(t, config, "- pod")
	assert.Contains(t, config, "- instance")
}
