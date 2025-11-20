package promquery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTestingT captures test failures for verification
type mockTestingT struct {
	failed   bool
	logs     []string
	errors   []string
	fatals   []string
	skipLogs []string
}

func (m *mockTestingT) Name() string {
	return "mock-test"
}

func (m *mockTestingT) Error(args ...interface{}) {
	m.failed = true
	m.errors = append(m.errors, fmt.Sprint(args...))
}

func (m *mockTestingT) Errorf(format string, args ...interface{}) {
	m.failed = true
	m.errors = append(m.errors, fmt.Sprintf(format, args...))
}

func (m *mockTestingT) Fail() {
	m.failed = true
}

func (m *mockTestingT) FailNow() {
	m.failed = true
	m.fatals = append(m.fatals, "FailNow called")
}

func (m *mockTestingT) Failed() bool {
	return m.failed
}

func (m *mockTestingT) Fatal(args ...interface{}) {
	m.failed = true
	m.fatals = append(m.fatals, fmt.Sprint(args...))
}

func (m *mockTestingT) Fatalf(format string, args ...interface{}) {
	m.failed = true
	m.fatals = append(m.fatals, fmt.Sprintf(format, args...))
}

func (m *mockTestingT) Helper() {
	// No-op for mock
}

func (m *mockTestingT) Log(args ...interface{}) {
	m.logs = append(m.logs, fmt.Sprint(args...))
}

func (m *mockTestingT) Logf(format string, args ...interface{}) {
	m.logs = append(m.logs, fmt.Sprintf(format, args...))
}

func (m *mockTestingT) Skip(args ...interface{}) {
	m.skipLogs = append(m.skipLogs, fmt.Sprint(args...))
}

func (m *mockTestingT) SkipNow() {
	m.skipLogs = append(m.skipLogs, "SkipNow called")
}

func (m *mockTestingT) Skipf(format string, args ...interface{}) {
	m.skipLogs = append(m.skipLogs, fmt.Sprintf(format, args...))
}

func (m *mockTestingT) Skipped() bool {
	return len(m.skipLogs) > 0
}

// Ensure mockTestingT implements the interface required by terratest/testing
var _ interface {
	Name() string
	Error(args ...interface{})
	Errorf(format string, args ...interface{})
	Fail()
	FailNow()
	Failed() bool
	Fatal(args ...interface{})
	Fatalf(format string, args ...interface{})
	Helper()
	Log(args ...interface{})
	Logf(format string, args ...interface{})
	Skip(args ...interface{})
	SkipNow()
	Skipf(format string, args ...interface{})
	Skipped() bool
} = (*mockTestingT)(nil)

func TestCheckNoAlertsFiring_NoAlerts(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v2/alerts", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckNoAlertsFiring(ctx, mockTest, "ns", []string{})

	assert.False(t, mockTest.failed, "Should not fail when no alerts are firing")
}

func TestCheckNoAlertsFiring_WithCustomExceptions(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `[
			{
				"labels": {
					"alertname": "CustomAlert"
				}
			}
		]`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckNoAlertsFiring(ctx, mockTest, "ns", []string{"CustomAlert"})

	assert.False(t, mockTest.failed, "Should not fail when only excepted alerts are firing")
}

func TestCheckNoAlertsFiring_WithFiringAlerts(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `[
			{
				"labels": {
					"alertname": "FiringAlert"
				}
			}
		]`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckNoAlertsFiring(ctx, mockTest, "ns", []string{})

	assert.True(t, mockTest.failed, "Should fail when alert is firing")
}

func TestCheckNoAlertsFiring_QueryError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckNoAlertsFiring(ctx, mockTest, "ns", []string{})

	assert.False(t, mockTest.failed, "Should not fail test on query error")
}

func TestCheckNoAlertsFiring_InvalidResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return invalid JSON for array (object instead of list)
		_, _ = fmt.Fprint(w, `{"status": "error"}`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckNoAlertsFiring(ctx, mockTest, "ns", []string{})

	assert.False(t, mockTest.failed, "Should not fail on invalid response")
}

func TestCheckAlertIsFiring_AlertFiring(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters := r.URL.Query()["filter"]
		assert.Contains(t, filters, "alertname=TargetAlert")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `[
			{
				"labels": {
					"alertname": "TargetAlert"
				}
			}
		]`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckAlertIsFiring(ctx, mockTest, "ns", "TargetAlert")

	assert.False(t, mockTest.failed, "Should not fail when target alert is firing")
}

func TestCheckAlertIsFiring_AlertNotFiring(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckAlertIsFiring(ctx, mockTest, "ns", "TargetAlert")

	assert.True(t, mockTest.failed, "Should fail when target alert is not firing")
}

func TestCheckAlertIsFiring_QueryError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := NewPrometheusClient(server.URL)
	require.NoError(t, err)
	client.AlertManagerURL = server.URL

	mockTest := &mockTestingT{}
	ctx := context.Background()

	client.CheckAlertIsFiring(ctx, mockTest, "ns", "TargetAlert")

	assert.True(t, mockTest.failed, "Should fail test on query error")
}
