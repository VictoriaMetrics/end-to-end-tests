package promquery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

const (
	queryTimeout  = 10 * time.Second
	queryStep     = 1 * time.Minute
	retryAttempts = 3
	retryDelay    = 2 * time.Second
)

// isLookupError returns true if err is a DNS lookup, timeout, or connection-refused/reset
// error - i.e. transient network conditions worth retrying. Other dial/read errors are
// treated as permanent failures so they surface immediately instead of being masked by retries.
func isLookupError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "read") {
		if errors.Is(opErr.Err, syscall.ECONNREFUSED) || errors.Is(opErr.Err, syscall.ECONNRESET) {
			return true
		}
	}
	return false
}

// withRetry runs call, retrying up to retryAttempts times (with retryDelay between attempts)
// while the returned error is a transient network error per isLookupError.
func withRetry[T any](ctx context.Context, call func(context.Context) (T, promv1.Warnings, error)) (T, promv1.Warnings, error) {
	var (
		val      T
		warnings promv1.Warnings
		err      error
	)
	for attempt := 0; attempt < retryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				var zero T
				return zero, nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		val, warnings, err = call(queryCtx)
		cancel()
		if err == nil || !isLookupError(err) {
			return val, warnings, err
		}
	}
	return val, warnings, err
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
	return withRetry(ctx, func(qctx context.Context) (prommodel.Value, promv1.Warnings, error) {
		return p.client.QueryRange(qctx, query, promv1.Range{
			Start: start,
			End:   end,
			Step:  queryStep,
		})
	})
}

// Query executes an instant Prometheus query at the current time.
// Retries on transient DNS/network errors up to retryAttempts times.
func (p PrometheusClient) Query(ctx context.Context, query string) (prommodel.Value, promv1.Warnings, error) {
	return withRetry(ctx, func(qctx context.Context) (prommodel.Value, promv1.Warnings, error) {
		return p.client.Query(qctx, query, time.Now())
	})
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
		return nil, 0, fmt.Errorf(consts.ErrNoDataReturned)
	}
	return vec[0].Metric, vec[0].Value, nil
}
