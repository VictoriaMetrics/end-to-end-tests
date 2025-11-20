package exporter

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthSerialization(t *testing.T) {
	testCases := []struct {
		name string
		auth Auth
	}{
		{
			name: "none auth",
			auth: Auth{Type: "none"},
		},
		{
			name: "basic auth",
			auth: Auth{Type: "basic"},
		},
		{
			name: "bearer auth",
			auth: Auth{Type: "bearer"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.auth)
			require.NoError(t, err, "Failed to marshal Auth")

			var unmarshaled Auth
			err = json.Unmarshal(data, &unmarshaled)
			require.NoError(t, err, "Failed to unmarshal Auth")

			assert.Equal(t, tc.auth.Type, unmarshaled.Type, "Auth type should match")
		})
	}
}

func TestConnectionSerialization(t *testing.T) {
	tenantID := 123
	conn := Connection{
		URL:           "http://victoria-metrics:8428",
		APIBasePath:   "/prometheus",
		TenantID:      &tenantID,
		IsMultitenant: true,
		FullAPIURL:    "http://victoria-metrics:8428/prometheus",
		Auth: Auth{
			Type: "basic",
		},
		SkipTLSVerify: true,
	}

	data, err := json.Marshal(conn)
	require.NoError(t, err, "Failed to marshal Connection")

	var unmarshaled Connection
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err, "Failed to unmarshal Connection")

	assert.Equal(t, conn.URL, unmarshaled.URL, "URL should match")
	assert.Equal(t, conn.APIBasePath, unmarshaled.APIBasePath, "APIBasePath should match")
	assert.Equal(t, *conn.TenantID, *unmarshaled.TenantID, "TenantID should match")
	assert.Equal(t, conn.IsMultitenant, unmarshaled.IsMultitenant, "IsMultitenant should match")
	assert.Equal(t, conn.FullAPIURL, unmarshaled.FullAPIURL, "FullAPIURL should match")
	assert.Equal(t, conn.Auth.Type, unmarshaled.Auth.Type, "Auth.Type should match")
	assert.Equal(t, conn.SkipTLSVerify, unmarshaled.SkipTLSVerify, "SkipTLSVerify should match")
}

func TestConnectionWithNullTenantID(t *testing.T) {
	conn := Connection{
		URL:           "http://victoria-metrics:8428",
		APIBasePath:   "/prometheus",
		TenantID:      nil, // Test null value
		IsMultitenant: false,
		FullAPIURL:    "http://victoria-metrics:8428/prometheus",
		Auth: Auth{
			Type: "none",
		},
		SkipTLSVerify: false,
	}

	data, err := json.Marshal(conn)
	require.NoError(t, err, "Failed to marshal Connection")

	var unmarshaled Connection
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err, "Failed to unmarshal Connection")

	assert.Nil(t, unmarshaled.TenantID, "TenantID should be nil")
}

func TestTimeRangeSerialization(t *testing.T) {
	start := time.Date(2023, 12, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2023, 12, 1, 11, 0, 0, 0, time.UTC)

	timeRange := TimeRange{
		Start: start,
		End:   end,
	}

	data, err := json.Marshal(timeRange)
	require.NoError(t, err, "Failed to marshal TimeRange")

	var unmarshaled TimeRange
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err, "Failed to unmarshal TimeRange")

	assert.True(t, unmarshaled.Start.Equal(timeRange.Start), "Start time should match")
	assert.True(t, unmarshaled.End.Equal(timeRange.End), "End time should match")
}

func TestObfuscationSerialization(t *testing.T) {
	obf := Obfuscation{
		Enabled:           true,
		ObfuscateInstance: false,
		ObfuscateJob:      true,
		PreserveStructure: false,
		CustomLabels:      []string{"label1", "label2", "label3"},
	}

	data, err := json.Marshal(obf)
	require.NoError(t, err, "Failed to marshal Obfuscation")

	var unmarshaled Obfuscation
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err, "Failed to unmarshal Obfuscation")

	assert.Equal(t, obf.Enabled, unmarshaled.Enabled, "Enabled should match")
	assert.Equal(t, obf.ObfuscateInstance, unmarshaled.ObfuscateInstance, "ObfuscateInstance should match")
	assert.Equal(t, obf.ObfuscateJob, unmarshaled.ObfuscateJob, "ObfuscateJob should match")
	assert.Equal(t, obf.PreserveStructure, unmarshaled.PreserveStructure, "PreserveStructure should match")
	assert.Len(t, unmarshaled.CustomLabels, len(obf.CustomLabels), "CustomLabels length should match")
	assert.Equal(t, obf.CustomLabels, unmarshaled.CustomLabels, "CustomLabels should match")
}

func TestBatchingSerialization(t *testing.T) {
	testCases := []struct {
		name     string
		batching Batching
	}{
		{
			name: "custom strategy",
			batching: Batching{
				Enabled:            true,
				Strategy:           "custom",
				CustomIntervalSecs: 60,
			},
		},
		{
			name: "disabled batching",
			batching: Batching{
				Enabled:            false,
				Strategy:           "",
				CustomIntervalSecs: 0,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.batching)
			require.NoError(t, err, "Failed to marshal Batching")

			var unmarshaled Batching
			err = json.Unmarshal(data, &unmarshaled)
			require.NoError(t, err, "Failed to unmarshal Batching")

			assert.Equal(t, tc.batching.Enabled, unmarshaled.Enabled, "Enabled should match")
			assert.Equal(t, tc.batching.Strategy, unmarshaled.Strategy, "Strategy should match")
			assert.Equal(t, tc.batching.CustomIntervalSecs, unmarshaled.CustomIntervalSecs, "CustomIntervalSecs should match")
		})
	}
}

func TestRequestBodySerialization(t *testing.T) {
	start := time.Date(2023, 12, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2023, 12, 1, 11, 0, 0, 0, time.UTC)
	tenantID := 42

	reqBody := RequestBody{
		Connection: Connection{
			URL:           "http://test.example.com",
			APIBasePath:   "/api/v1",
			TenantID:      &tenantID,
			IsMultitenant: true,
			FullAPIURL:    "http://test.example.com/api/v1",
			Auth:          Auth{Type: "bearer"},
			SkipTLSVerify: true,
		},
		TimeRange: TimeRange{
			Start: start,
			End:   end,
		},
		Components: []string{"operator", "victoria"},
		Jobs:       []string{"job1", "job2"},
		Obfuscation: Obfuscation{
			Enabled:           false,
			ObfuscateInstance: true,
			ObfuscateJob:      false,
			PreserveStructure: true,
			CustomLabels:      []string{"custom1", "custom2"},
		},
		StagingDir:        "/tmp/staging",
		MetricStepSeconds: 30,
		Batching: Batching{
			Enabled:            true,
			Strategy:           "time",
			CustomIntervalSecs: 120,
		},
	}

	data, err := json.Marshal(reqBody)
	require.NoError(t, err, "Failed to marshal RequestBody")

	var unmarshaled RequestBody
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err, "Failed to unmarshal RequestBody")

	// Test Connection
	assert.Equal(t, reqBody.Connection.URL, unmarshaled.Connection.URL, "Connection.URL should match")
	assert.Equal(t, *reqBody.Connection.TenantID, *unmarshaled.Connection.TenantID, "Connection.TenantID should match")

	// Test TimeRange
	assert.True(t, unmarshaled.TimeRange.Start.Equal(reqBody.TimeRange.Start), "TimeRange.Start should match")
	assert.True(t, unmarshaled.TimeRange.End.Equal(reqBody.TimeRange.End), "TimeRange.End should match")

	// Test Components
	assert.Len(t, unmarshaled.Components, len(reqBody.Components), "Components length should match")
	assert.Equal(t, reqBody.Components, unmarshaled.Components, "Components should match")

	// Test Jobs
	assert.Len(t, unmarshaled.Jobs, len(reqBody.Jobs), "Jobs length should match")
	assert.Equal(t, reqBody.Jobs, unmarshaled.Jobs, "Jobs should match")

	// Test StagingDir
	assert.Equal(t, reqBody.StagingDir, unmarshaled.StagingDir, "StagingDir should match")

	// Test MetricStepSeconds
	assert.Equal(t, reqBody.MetricStepSeconds, unmarshaled.MetricStepSeconds, "MetricStepSeconds should match")
}

func TestEmptySlicesSerialization(t *testing.T) {
	reqBody := RequestBody{
		Connection: Connection{
			URL:           "http://test.example.com",
			APIBasePath:   "/api/v1",
			TenantID:      nil,
			IsMultitenant: false,
			FullAPIURL:    "http://test.example.com/api/v1",
			Auth:          Auth{Type: "none"},
			SkipTLSVerify: false,
		},
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Hour),
			End:   time.Now(),
		},
		Components: []string{}, // Empty slice
		Jobs:       []string{}, // Empty slice
		Obfuscation: Obfuscation{
			Enabled:           false,
			ObfuscateInstance: false,
			ObfuscateJob:      false,
			PreserveStructure: true,
			CustomLabels:      []string{}, // Empty slice
		},
		StagingDir:        "/tmp",
		MetricStepSeconds: 0,
		Batching: Batching{
			Enabled:            false,
			Strategy:           "",
			CustomIntervalSecs: 0,
		},
	}

	data, err := json.Marshal(reqBody)
	require.NoError(t, err, "Failed to marshal RequestBody with empty slices")

	var unmarshaled RequestBody
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err, "Failed to unmarshal RequestBody with empty slices")

	assert.Empty(t, unmarshaled.Components, "Components slice should be empty")
	assert.Empty(t, unmarshaled.Jobs, "Jobs slice should be empty")
	assert.Empty(t, unmarshaled.Obfuscation.CustomLabels, "CustomLabels slice should be empty")
}

// TestJSONMarshallingEdgeCases tests various edge cases for JSON marshalling
func TestJSONMarshallingEdgeCases(t *testing.T) {
	t.Run("invalid JSON", func(t *testing.T) {
		var conn Connection
		invalidJSON := `{"url": "invalid json`
		err := json.Unmarshal([]byte(invalidJSON), &conn)
		assert.Error(t, err, "Should fail on invalid JSON")
	})

	t.Run("zero time values", func(t *testing.T) {
		timeRange := TimeRange{
			Start: time.Time{},
			End:   time.Time{},
		}

		data, err := json.Marshal(timeRange)
		require.NoError(t, err, "Should marshal zero time values")

		var unmarshaled TimeRange
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err, "Should unmarshal zero time values")

		assert.True(t, unmarshaled.Start.IsZero(), "Start should be zero time")
		assert.True(t, unmarshaled.End.IsZero(), "End should be zero time")
	})
}
