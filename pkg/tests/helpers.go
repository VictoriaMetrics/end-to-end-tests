package tests

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2" //nolint:stylecheck,staticcheck
	prommodel "github.com/prometheus/common/model"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/gather"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
)

var OverwatchStart time.Time

func SetupOverwatchClient(ctx context.Context, t terratesting.TestingT) (promquery.PrometheusClient, error) {
	install.DiscoverIngressHost(ctx, t)
	overwatchURL := helpers.OverwatchURL()
	logger.Default.Logf(t, "Running overwatch at %s", consts.VMSingleUrl())
	client, err := promquery.NewPrometheusClient(overwatchURL)
	if err != nil {
		return promquery.PrometheusClient{}, fmt.Errorf("failed to create overwatch client: %w", err)
	}
	startTime := time.Now()
	client.Start = startTime
	OverwatchStart = startTime
	return client, nil
}

func NewHTTPClient() *http.Client { return helpers.NewHTTPClient() }
func NewHTTPClientWithTimeout(timeout time.Duration) *http.Client {
	return helpers.NewHTTPClientWithTimeout(timeout)
}
func RandomNamespace(prefix string) string { return helpers.RandomNamespace(prefix) }
func ClusterName(prefix string) string     { return helpers.ClusterName(prefix) }
func VMClusterAffinity(clusterName, namespaceLabel string) map[string]interface{} {
	return helpers.VMClusterAffinity(clusterName, namespaceLabel)
}
func VLClusterAffinity(clusterName, namespaceLabel string) map[string]interface{} {
	return helpers.VLClusterAffinity(clusterName, namespaceLabel)
}
func CleanupNamespace(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	helpers.CleanupNamespace(t, kubeOpts, namespace)
}
func EnsureNamespaceExists(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	helpers.EnsureNamespaceExists(t, kubeOpts, namespace)
}

func GatherOnFailure(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	GatherOnFailureFrom(ctx, t, kubeOpts, namespace, OverwatchStart)
}

func GatherOnFailureFrom(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, startTime time.Time) {
	if CurrentSpecReport().Failed() {
		gather.VMAfterAll(ctx, t, startTime, consts.ResourceWaitTimeout, namespace)
		gather.VLAfterAll(ctx, t, startTime, consts.ResourceWaitTimeout)
		gather.K8sAfterAll(ctx, t, kubeOpts, consts.ResourceWaitTimeout)
	}
}

func NewTenantPromClient(t terratesting.TestingT, namespace string, tenantID int, startTime time.Time) (promquery.PrometheusClient, error) {
	client, err := promquery.NewPrometheusClient(helpers.TenantSelectURL(namespace, tenantID))
	if err != nil {
		return promquery.PrometheusClient{}, err
	}
	client.Start = startTime
	return client, nil
}
func NewPromClientWithURL(url string, startTime time.Time) (promquery.PrometheusClient, error) {
	client, err := promquery.NewPrometheusClient(url)
	if err != nil {
		return promquery.PrometheusClient{}, err
	}
	client.Start = startTime
	return client, nil
}
func NewMultitenantPromClient(t terratesting.TestingT, namespace string, startTime time.Time) (promquery.PrometheusClient, error) {
	return NewPromClientWithURL(helpers.MultitenantSelectURL(namespace), startTime)
}

func OverwatchURL() string { return helpers.OverwatchURL() }
func TenantInsertURL(namespace string, tenantID int) string {
	return helpers.TenantInsertURL(namespace, tenantID)
}
func TenantSelectURL(namespace string, tenantID int) string {
	return helpers.TenantSelectURL(namespace, tenantID)
}
func MultitenantInsertURL(namespace string) string { return helpers.MultitenantInsertURL(namespace) }
func MultitenantSelectURL(namespace string) string { return helpers.MultitenantSelectURL(namespace) }
func VMSingleRemoteWriteURL(namespace string) string {
	return helpers.VMSingleRemoteWriteURL(namespace)
}
func VMSinglePrometheusURL(namespace string) string { return helpers.VMSinglePrometheusURL(namespace) }
func VMAgentRemoteWriteURL(namespace string) string { return helpers.VMAgentRemoteWriteURL(namespace) }
func VMAgentNamedRemoteWriteURL(name, namespace string) string {
	return helpers.VMAgentNamedRemoteWriteURL(name, namespace)
}
func VMAgentNamedImportURL(name, namespace string) string {
	return helpers.VMAgentNamedImportURL(name, namespace)
}
func GlobalInsertURL(namespace string) string { return helpers.GlobalInsertURL(namespace) }
func GlobalSelectURL(namespace string) string { return helpers.GlobalSelectURL(namespace) }
func ZoneSelectURL(zone string) string        { return helpers.ZoneSelectURL(zone) }
func WaitForDataPropagation()                 { helpers.WaitForDataPropagation() }
func WaitForAggregation()                     { helpers.WaitForAggregation() }

type ChaosMeshConfig = helpers.ChaosMeshConfig

func DefaultChaosMeshConfig() ChaosMeshConfig { return helpers.DefaultChaosMeshConfig() }

func RetryVectorScan(ctx context.Context, t terratesting.TestingT, namespace string, prom promquery.PrometheusClient, query string, maxRetries int) (prommodel.Metric, prommodel.SampleValue, error) {
	return helpers.RetryVectorScan(ctx, t, namespace, prom, query, maxRetries)
}
