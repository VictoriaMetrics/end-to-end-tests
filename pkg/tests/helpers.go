package tests

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2" //nolint:stylecheck,staticcheck

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/gather"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
)

// OverwatchStart records the time tests started collecting metrics via overwatch.
// It is set by SetupOverwatchClient so other helpers/builders can reuse the same
// start timestamp for queries.
var OverwatchStart time.Time

// SetupOverwatchClient initializes the overwatch Prometheus client with common configuration.
// It sets the package-level OverwatchStart variable and returns the initialized client.
// Tests should call this at the beginning of their setup and use the returned client.
func SetupOverwatchClient(ctx context.Context, t terratesting.TestingT) (promquery.PrometheusClient, error) {
	install.DiscoverIngressHost(ctx, t)

	overwatchURL := OverwatchURL()
	logger.Default.Logf(t, "Running overwatch at %s", consts.VMSingleUrl())

	client, err := promquery.NewPrometheusClient(overwatchURL)
	if err != nil {
		return promquery.PrometheusClient{}, fmt.Errorf("failed to create overwatch client: %w", err)
	}

	startTime := time.Now()
	client.Start = startTime

	// Persist start time for test-level reuse
	OverwatchStart = startTime

	return client, nil
}

// NewHTTPClient creates a new HTTP client with the default timeout.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: consts.HTTPClientTimeout,
	}
}

// NewHTTPClientWithTimeout creates a new HTTP client with a custom timeout.
func NewHTTPClientWithTimeout(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
	}
}

// RandomNamespace generates a unique namespace name with random suffix.
// This ensures tests running in parallel don't conflict with each other.
func RandomNamespace(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, randomString(6))
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	ret := make([]byte, n)
	for i := 0; i < n; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			panic(err)
		}
		ret[i] = letters[num.Int64()]
	}
	return string(ret)
}

// CleanupNamespace deletes a namespace, ignoring if it doesn't exist.
func CleanupNamespace(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	k8s.RunKubectl(t, kubeOpts, "delete", "namespace", namespace, "--ignore-not-found=true")
}

// EnsureNamespaceExists creates a namespace if it doesn't already exist.
func EnsureNamespaceExists(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	if _, err := k8s.GetNamespaceE(t, kubeOpts, namespace); err != nil {
		k8s.CreateNamespace(t, kubeOpts, namespace)
	}
}

// GatherOnFailure collects diagnostic information if the current test has failed.
// This should be called in AfterEach blocks.
func GatherOnFailure(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, releaseName string) {
	if CurrentSpecReport().Failed() {
		gather.VMAfterAll(ctx, t, consts.ResourceWaitTimeout, releaseName)
		gather.K8sAfterAll(ctx, t, kubeOpts, consts.ResourceWaitTimeout)
	}
}

// NewTenantPromClient creates a new Prometheus client for a specific tenant.
// The startTime is typically obtained from the overwatch setup.
func NewTenantPromClient(t terratesting.TestingT, namespace string, tenantID int, startTime time.Time) (promquery.PrometheusClient, error) {
	selectURL := TenantSelectURL(namespace, tenantID)
	client, err := promquery.NewPrometheusClient(selectURL)
	if err != nil {
		return promquery.PrometheusClient{}, err
	}
	client.Start = startTime
	return client, nil
}

// NewPromClientWithURL creates a new Prometheus client with a custom URL.
func NewPromClientWithURL(url string, startTime time.Time) (promquery.PrometheusClient, error) {
	client, err := promquery.NewPrometheusClient(url)
	if err != nil {
		return promquery.PrometheusClient{}, err
	}
	client.Start = startTime
	return client, nil
}

// NewMultitenantPromClient creates a new Prometheus client for the multitenant endpoint.
func NewMultitenantPromClient(t terratesting.TestingT, namespace string, startTime time.Time) (promquery.PrometheusClient, error) {
	selectURL := MultitenantSelectURL(namespace)
	return NewPromClientWithURL(selectURL, startTime)
}

// URL building helpers

// OverwatchURL returns the URL for the overwatch Prometheus endpoint.
func OverwatchURL() string {
	return fmt.Sprintf("%s%s", consts.VMSingleUrl(), consts.PrometheusPathSuffix)
}

// TenantInsertURL returns the URL for inserting data into a specific tenant.
func TenantInsertURL(namespace string, tenantID int) string {
	return fmt.Sprintf("http://%s"+consts.TenantInsertPathFormat, consts.VMInsertHost(namespace), tenantID)
}

// TenantSelectURL returns the URL for querying data from a specific tenant.
func TenantSelectURL(namespace string, tenantID int) string {
	return fmt.Sprintf("%s"+consts.TenantSelectPathFormat, consts.VMSelectUrl(namespace), tenantID)
}

// MultitenantInsertURL returns the URL for the multitenant insert endpoint.
func MultitenantInsertURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMInsertHost(namespace), consts.MultitenantInsertPath)
}

// MultitenantSelectURL returns the URL for the multitenant select endpoint.
func MultitenantSelectURL(namespace string) string {
	return fmt.Sprintf("%s%s", consts.VMSelectUrl(namespace), consts.MultitenantSelectPath)
}

// VMSingleRemoteWriteURL returns the remote write URL for a namespaced VMSingle.
func VMSingleRemoteWriteURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMSingleNamespacedHost(namespace), consts.RemoteWritePath)
}

// VMSinglePrometheusURL returns the Prometheus query URL for a namespaced VMSingle.
func VMSinglePrometheusURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMSingleNamespacedHost(namespace), consts.PrometheusPathSuffix)
}

// VMAgentRemoteWriteURL returns the remote write URL for a namespaced VMAgent.
func VMAgentRemoteWriteURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMAgentNamespacedHost(namespace), consts.RemoteWritePath)
}

// GlobalInsertURL returns the global insert URL for distributed deployments.
func GlobalInsertURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMInsertHost(namespace), consts.RemoteWritePath)
}

// GlobalSelectURL returns the global select URL for distributed deployments.
func GlobalSelectURL(namespace string) string {
	return fmt.Sprintf("%s/select/0/prometheus", consts.VMSelectUrl(namespace))
}

// ZoneSelectURL returns the zone-specific select URL for distributed deployments.
func ZoneSelectURL(zone string) string {
	return fmt.Sprintf("http://vmselect-%s.%s.nip.io/select/0/prometheus", zone, consts.NginxHost())
}

// Assertion helpers

// RequireNoError is a helper that wraps require.NoError with consistent behavior.
func RequireNoError(t terratesting.TestingT, err error, msgAndArgs ...interface{}) {
	require.NoError(t, err, msgAndArgs...)
}

// WaitForDataPropagation waits for the standard data propagation delay.
func WaitForDataPropagation() {
	time.Sleep(consts.DataPropagationDelay)
}

// WaitForAggregation waits for the streaming aggregation interval to pass.
func WaitForAggregation() {
	time.Sleep(consts.AggregationWaitTime)
}

// Common test setup configurations

// VMK8sStackConfig holds configuration for installing VM K8s stack.
type VMK8sStackConfig struct {
	HelmChart   string
	ValuesFile  string
	Namespace   string
	ReleaseName string
}

// DefaultVMK8sStackConfig returns the default configuration for VM K8s stack.
func DefaultVMK8sStackConfig() VMK8sStackConfig {
	return VMK8sStackConfig{
		HelmChart:   consts.VMK8sStackChart,
		ValuesFile:  consts.SmokeValuesFile,
		Namespace:   consts.DefaultVMNamespace,
		ReleaseName: consts.DefaultReleaseName,
	}
}

// PromBenchmarkConfig holds configuration for prometheus benchmark.
type PromBenchmarkConfig struct {
	DisableMonitoring bool
	TargetsCount      string
	WriteURL          string
	ReadURL           string
}

// ToHelmValues converts the config to Helm values map.
func (c PromBenchmarkConfig) ToHelmValues() map[string]string {
	values := map[string]string{
		"disableMonitoring":          fmt.Sprintf("%t", c.DisableMonitoring),
		"targetsCount":               c.TargetsCount,
		"remoteStorages.vm.writeURL": c.WriteURL,
		"remoteStorages.vm.readURL":  c.ReadURL,
	}

	return values
}

// ChaosMeshConfig holds configuration for chaos mesh installation.
type ChaosMeshConfig struct {
	HelmChart   string
	ValuesFile  string
	Namespace   string
	ReleaseName string
}

// DefaultChaosMeshConfig returns the default configuration for chaos mesh.
func DefaultChaosMeshConfig() ChaosMeshConfig {
	return ChaosMeshConfig{
		HelmChart:   consts.ChaosMeshChart,
		ValuesFile:  consts.ChaosMeshValuesFile,
		Namespace:   consts.ChaosMeshNamespace,
		ReleaseName: consts.ChaosMeshReleaseName,
	}
}
