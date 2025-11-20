package promquery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	prommodel "github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPrometheusClient(t *testing.T) {
	t.Parallel()
	// Test valid URL
	client, err := NewPrometheusClient("http://localhost:9090")
	require.NoError(t, err, "Expected no error creating client")
	assert.NotNil(t, client.client, "Expected client to be initialized")

	// Test invalid URL
	_, err = NewPrometheusClient("://invalid-url")
	assert.Error(t, err, "Expected error for invalid URL")
}

func TestVectorScan(t *testing.T) {
	t.Parallel()
	// Create a mock server that returns different types of responses
	tests := []struct {
		name           string
		response       string
		expectedMetric prommodel.Metric
		expectedValue  prommodel.SampleValue
		expectedError  bool
		errorContains  string
	}{
		{
			name: "valid vector response",
			response: `{
				"status": "success",
				"data": {
					"resultType": "vector",
					"result": [
						{
							"metric": {"__name__": "test_metric", "label1": "value1"},
							"value": [1234567890, "42.5"]
						}
					]
				}
			}`,
			expectedMetric: prommodel.Metric{"__name__": "test_metric", "label1": "value1"},
			expectedValue:  42.5,
			expectedError:  false,
		},
		{
			name: "empty vector response",
			response: `{
				"status": "success",
				"data": {
					"resultType": "vector",
					"result": []
				}
			}`,
			expectedError: true,
			errorContains: "no data returned",
		},
		{
			name: "matrix response (wrong type)",
			response: `{
				"status": "success",
				"data": {
					"resultType": "matrix",
					"result": []
				}
			}`,
			expectedError: true,
			errorContains: "unexpected result type: matrix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, tt.response)
			}))
			defer server.Close()

			// Create client
			client, err := NewPrometheusClient(server.URL)
			require.NoError(t, err, "Failed to create client")

			// Test VectorScan
			metric, value, err := client.VectorScan(context.Background(), "test_query")

			if tt.expectedError {
				require.Error(t, err, "Expected error but got none")
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, "Error message mismatch")
				}
				return
			}

			require.NoError(t, err, "Unexpected error")
			assert.Equal(t, tt.expectedValue, value, "Expected value mismatch")
			assert.Equal(t, tt.expectedMetric, metric, "Expected metric mismatch")
		})
	}
}

func TestQuery(t *testing.T) {
	t.Parallel()
	// Create a mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request parameters
		assert.Equal(t, "POST", r.Method, "Expected POST request")
		assert.Equal(t, "/api/v1/query", r.URL.Path, "Expected path '/api/v1/query'")

		// Parse form data
		err := r.ParseForm()
		require.NoError(t, err, "Failed to parse form")

		query := r.Form.Get("query")
		assert.Equal(t, "test_metric", query, "Expected query 'test_metric'")

		// Return a mock response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{
			"status": "success",
			"data": {
				"resultType": "vector",
				"result": [
					{
						"metric": {"__name__": "test_metric"},
						"value": [1234567890, "10"]
					}
				]
			}
		}`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err, "Failed to create client")

	ctx := context.Background()
	result, warnings, err := client.Query(ctx, "test_metric")

	require.NoError(t, err, "Query failed")
	require.NotNil(t, result, "Expected non-nil result")
	assert.Empty(t, warnings, "Unexpected warnings")
	assert.Equal(t, prommodel.ValVector, result.Type(), "Expected vector result")
}

func TestQueryRange(t *testing.T) {
	t.Parallel()
	// Create a mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request parameters
		assert.Equal(t, "POST", r.Method, "Expected POST request")
		assert.Equal(t, "/api/v1/query_range", r.URL.Path, "Expected path '/api/v1/query_range'")

		// Parse form data
		err := r.ParseForm()
		require.NoError(t, err, "Failed to parse form")

		query := r.Form.Get("query")
		assert.Equal(t, "test_range_metric", query, "Expected query 'test_range_metric'")

		// Check that start, end, and step parameters are present
		assert.NotEmpty(t, r.Form.Get("start"), "Expected start parameter")
		assert.NotEmpty(t, r.Form.Get("end"), "Expected end parameter")
		assert.NotEmpty(t, r.Form.Get("step"), "Expected step parameter")

		// Return a mock response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{
			"status": "success",
			"data": {
				"resultType": "matrix",
				"result": [
					{
						"metric": {"__name__": "test_range_metric"},
						"values": [
							[1234567890, "10"],
							[1234567920, "20"]
						]
					}
				]
			}
		}`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err, "Failed to create client")

	// Set start time for the client
	client.Start = time.Now().Add(-1 * time.Hour)

	ctx := context.Background()
	result, warnings, err := client.QueryRange(ctx, "test_range_metric")

	require.NoError(t, err, "QueryRange failed")
	require.NotNil(t, result, "Expected non-nil result")
	assert.Empty(t, warnings, "Unexpected warnings")
	assert.Equal(t, prommodel.ValMatrix, result.Type(), "Expected matrix result")
}

func TestQueryWithTimeout(t *testing.T) {
	t.Parallel()
	// Create a slow server that takes longer than a custom short timeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Short delay for testing
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err, "Failed to create client")

	// Use a custom timeout that's shorter than the server delay
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, _, err = client.Query(ctx, "slow_query")

	require.Error(t, err, "Expected timeout error")
	// The error should contain context deadline exceeded
	assert.True(t, strings.Contains(err.Error(), "context deadline exceeded"), "Expected context deadline exceeded error, got: %v", err)
}

func TestQueryRangeWithTimeout(t *testing.T) {
	t.Parallel()
	// Create a slow server that takes longer than a custom short timeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Short delay for testing
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err, "Failed to create client")

	// Set start time for the client
	client.Start = time.Now().Add(-1 * time.Hour)

	// Use a custom timeout that's shorter than the server delay
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, _, err = client.QueryRange(ctx, "slow_range_query")

	require.Error(t, err, "Expected timeout error")
}

func TestClientStartTime(t *testing.T) {
	t.Parallel()
	client, err := NewPrometheusClient("http://localhost:9090")
	require.NoError(t, err, "Failed to create client")

	// Test that Start time can be set and retrieved
	testTime := time.Date(2023, 12, 1, 10, 0, 0, 0, time.UTC)
	client.Start = testTime

	assert.True(t, client.Start.Equal(testTime), "Expected Start time to match")
}
