package promquery

import (
	"context"
	"fmt"
	"net"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
)

const (
	queryTimeout  = 10 * time.Second
	queryStep     = 1 * time.Minute
	retryAttempts = 3
	retryDelay    = 2 * time.Second
)

// isLookupError returns true if err is a DNS lookup / network transient error.
func isLookupError(err error) bool {
	if err == nil {
		return false
	}
	if netErr, ok := err.(net.Error); ok {
		if netErr.Timeout() {
			return true
		}
	}
	if opErr, ok := err.(*net.OpError); ok {
		if opErr.Op == "dial" || opErr.Op == "read" {
			return true
		}
		if _, ok := opErr.Err.(*net.DNSError); ok {
			return true
		}
	}
	return false
}

// PrometheusClient is a wrapper around the Prometheus API client.
// It keeps track of a Start time for range queries.
type PrometheusClient struct {
	client promv1.API
	Start  time.Time
	// AlertManagerURL is the URL of the Alertmanager to use for alert checks.
	// If empty, the URL is derived from the namespace.
	AlertManagerURL string
}

// NewPrometheusClient creates a new PrometheusClient for the given URL.
func NewPrometheusClient(url string) (PrometheusClient, error) {
	promClient, err := promapi.NewClient(promapi.Config{
		Address: url,
	})
	if err != nil {
		return PrometheusClient{}, err
	}
	promv1api := promv1.NewAPI(promClient)
	return PrometheusClient{client: promv1api}, nil
}

// QueryRange executes a Prometheus range query from p.Start to now.
// Retries on transient DNS/network errors up to retryAttempts times.
func (p PrometheusClient) QueryRange(ctx context.Context, query string) (prommodel.Value, promv1.Warnings, error) {
	return p.QueryRangeAt(ctx, query, p.Start, time.Now())
}

// QueryRangeAt executes a Prometheus range query for a fixed time window.
// Retries on transient DNS/network errors up to retryAttempts times.
func (p PrometheusClient) QueryRangeAt(ctx context.Context, query string, start, end time.Time) (prommodel.Value, promv1.Warnings, error) {
	var (
		val      prommodel.Value
		warnings promv1.Warnings
		err      error
	)
	for attempt := 0; attempt < retryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		val, warnings, err = p.client.QueryRange(queryCtx, query, promv1.Range{
			Start: start,
			End:   end,
			Step:  queryStep,
		})
		cancel()
		if err == nil || !isLookupError(err) {
			return val, warnings, err
		}
	}
	return val, warnings, err
}

// Query executes an instant Prometheus query at the current time.
// Retries on transient DNS/network errors up to retryAttempts times.
func (p PrometheusClient) Query(ctx context.Context, query string) (prommodel.Value, promv1.Warnings, error) {
	var (
		val      prommodel.Value
		warnings promv1.Warnings
		err      error
	)
	for attempt := 0; attempt < retryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		val, warnings, err = p.client.Query(queryCtx, query, time.Now())
		cancel()
		if err == nil || !isLookupError(err) {
			return val, warnings, err
		}
	}
	return val, warnings, err
}

// VectorScan executes an instant query and returns the first sample's metric and value from the result vector.
// It returns an error if the query fails, returns no data, or returns a non-vector result.
func (p PrometheusClient) VectorScan(ctx context.Context, query string) (prommodel.Metric, prommodel.SampleValue, error) {
	result, _, err := p.Query(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	if result.Type() != prommodel.ValVector {
		return nil, 0, fmt.Errorf("unexpected result type: %s", result.Type())
	}
	vec := result.(prommodel.Vector)
	if len(vec) == 0 {
		return nil, 0, fmt.Errorf("no data returned")
	}
	return vec[0].Metric, vec[0].Value, nil
}
