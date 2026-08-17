package helpers

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
	"github.com/prometheus/common/model"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
)

func NewHTTPClient() *http.Client { return NewHTTPClientWithTimeout(consts.HTTPClientTimeout) }

func NewHTTPClientWithTimeout(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

const randomNameSuffixLength = 6
const maxClusterNameLen = 42
const maxNamespaceLen = 51

func RandomNamespace(prefix string) string { return randomName(prefix, maxNamespaceLen) }

func ClusterName(prefix string) string {
	if len(prefix) > maxClusterNameLen {
		return prefix[:maxClusterNameLen]
	}
	return prefix
}

func VMClusterAffinity(clusterName, namespaceLabel string) map[string]interface{} {
	return clusterAffinity(clusterName, namespaceLabel, []string{"vminsert", "vmselect", "vmstorage"})
}

func VLClusterAffinity(clusterName, namespaceLabel string) map[string]interface{} {
	return clusterAffinity(clusterName, namespaceLabel, []string{"vlinsert", "vlselect", "vlstorage"})
}

func clusterAffinity(clusterName, namespaceLabel string, componentNames []string) map[string]interface{} {
	return map[string]interface{}{
		"podAffinity": map[string]interface{}{
			"requiredDuringSchedulingIgnoredDuringExecution": []map[string]interface{}{{
				"topologyKey": "kubernetes.io/hostname",
				"labelSelector": map[string]interface{}{
					"matchExpressions": []map[string]interface{}{{
						"key": "app.kubernetes.io/instance", "operator": "In", "values": []string{clusterName},
					}},
				},
			}},
		},
		"podAntiAffinity": map[string]interface{}{
			"requiredDuringSchedulingIgnoredDuringExecution": []map[string]interface{}{{
				"topologyKey":       "kubernetes.io/hostname",
				"namespaceSelector": map[string]interface{}{"matchLabels": map[string]interface{}{namespaceLabel: "true"}},
				"labelSelector": map[string]interface{}{
					"matchExpressions": []map[string]interface{}{
						{"key": "app.kubernetes.io/instance", "operator": "Exists"},
						{"key": "app.kubernetes.io/instance", "operator": "NotIn", "values": []string{clusterName}},
						{"key": "app.kubernetes.io/name", "operator": "In", "values": componentNames},
					},
				},
			}},
		},
	}
}

func randomName(prefix string, maxLen int) string {
	suffix := randomString(randomNameSuffixLength)
	maxPrefixLen := maxLen - len(suffix) - 1
	if len(prefix) > maxPrefixLen {
		prefix = prefix[:maxPrefixLen]
	}
	return fmt.Sprintf("%s-%s", prefix, suffix)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	ret := make([]byte, n)
	for i := range ret {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			panic(err)
		}
		ret[i] = letters[num.Int64()]
	}
	return string(ret)
}

func CleanupNamespace(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "namespace", namespace,
		"--ignore-not-found=true", "--wait=true", fmt.Sprintf("--timeout=%s", consts.PollingTimeout))
}

func EnsureNamespaceExists(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	if _, err := k8s.GetNamespaceContextE(t, context.Background(), kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, context.Background(), kubeOpts, namespace)
		k8s.RunKubectlContext(t, context.Background(), kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}
}

func OverwatchURL() string {
	return fmt.Sprintf("%s%s", consts.VMSingleUrl(), consts.PrometheusPathSuffix)
}
func TenantInsertURL(namespace string, tenantID int) string {
	return fmt.Sprintf("http://%s"+consts.TenantInsertPathFormat, consts.VMInsertHost(namespace), tenantID)
}
func TenantSelectURL(namespace string, tenantID int) string {
	return fmt.Sprintf("%s"+consts.TenantSelectPathFormat, consts.VMSelectUrl(namespace), tenantID)
}
func MultitenantInsertURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMInsertHost(namespace), consts.MultitenantInsertPath)
}
func MultitenantSelectURL(namespace string) string {
	return fmt.Sprintf("%s%s", consts.VMSelectUrl(namespace), consts.MultitenantSelectPath)
}
func VMSingleRemoteWriteURL(namespace string) string {
	return fmt.Sprintf("http://%s%s%s", consts.VMSingleNamespacedHost(namespace), consts.PrometheusPathSuffix, consts.RemoteWritePath)
}
func VMSinglePrometheusURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMSingleNamespacedHost(namespace), consts.PrometheusPathSuffix)
}
func VMAgentRemoteWriteURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMAgentNamespacedHost(namespace), consts.RemoteWritePath)
}
func VMAgentNamedRemoteWriteURL(name, namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMAgentNamedHost(name, namespace), consts.RemoteWritePath)
}
func VMAgentNamedImportURL(name, namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMAgentNamedHost(name, namespace), consts.ImportPrometheusPath)
}
func GlobalInsertURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMInsertHost(namespace), consts.RemoteWritePath)
}
func GlobalSelectURL(namespace string) string {
	return fmt.Sprintf("%s/select/0/prometheus", consts.VMSelectUrl(namespace))
}
func ZoneSelectURL(zone string) string {
	return fmt.Sprintf("http://vmselect-%s.%s.nip.io/select/0/prometheus", zone, consts.IngressHost())
}

func WaitForDataPropagation() { time.Sleep(consts.DataPropagationDelay) }
func WaitForAggregation()     { time.Sleep(consts.AggregationWaitTime) }

type ChaosMeshConfig struct {
	HelmChart   string
	ValuesFile  string
	Namespace   string
	ReleaseName string
}

func DefaultChaosMeshConfig() ChaosMeshConfig {
	return ChaosMeshConfig{HelmChart: consts.ChaosMeshChart, ValuesFile: consts.ChaosMeshValuesFile(), Namespace: consts.ChaosMeshNamespace, ReleaseName: consts.ChaosMeshReleaseName}
}

func RetryVectorScan(ctx context.Context, t terratesting.TestingT, namespace string, prom promquery.PrometheusClient, query string, maxRetries int) (model.Metric, model.SampleValue, error) {
	var lastErr error
	var lastMetric model.Metric
	var lastValue model.SampleValue
	for i := 0; i < maxRetries; i++ {
		metric, value, err := prom.VectorScan(ctx, query)
		lastErr, lastMetric, lastValue = err, metric, value
		if err == nil {
			return metric, value, nil
		}
		logger.Default.Logf(t, "Attempt %d: VectorScan for %q failed: %v", i+1, query, err)
		WaitForDataPropagation()
	}
	if lastErr != nil {
		logger.Default.Logf(t, "Final VectorScan failure for %q: %v", query, lastErr)
	}
	return lastMetric, lastValue, lastErr
}
