package promquery

import (
	"context"
	"fmt"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
)

const (
	queryTimeout = 10 * time.Second
	queryStep    = 1 * time.Minute
)

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
func (p PrometheusClient) QueryRange(ctx context.Context, query string) (prommodel.Value, promv1.Warnings, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	return p.client.QueryRange(ctx, query, promv1.Range{
		Start: p.Start,
		End:   time.Now(),
		Step:  queryStep,
	})
}

// Query executes an instant Prometheus query at the current time.
func (p PrometheusClient) Query(ctx context.Context, query string) (prommodel.Value, promv1.Warnings, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	return p.client.Query(ctx, query, time.Now())
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
