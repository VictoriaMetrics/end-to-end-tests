package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/prompb"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/remotewrite"
)

// ConfigMapBuilder provides a fluent interface for building Kubernetes ConfigMaps.
type ConfigMapBuilder struct {
	name string
	data map[string]string
}

// NewConfigMapBuilder creates a new ConfigMapBuilder with the given name.
func NewConfigMapBuilder(name string) *ConfigMapBuilder {
	return &ConfigMapBuilder{
		name: name,
		data: make(map[string]string),
	}
}

// WithRelabelConfig adds a relabel configuration to the ConfigMap.
func (b *ConfigMapBuilder) WithRelabelConfig(config string) *ConfigMapBuilder {
	b.data["relabel.yml"] = config
	return b
}

// WithStreamAggrConfig adds a streaming aggregation configuration to the ConfigMap.
func (b *ConfigMapBuilder) WithStreamAggrConfig(config string) *ConfigMapBuilder {
	b.data["stream-aggr.yml"] = config
	return b
}

func (b *ConfigMapBuilder) build() *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: b.name,
		},
		Data: b.data,
	}
	return cm
}

// Apply creates the ConfigMap in the cluster.
func (b *ConfigMapBuilder) Apply(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) error {
	cm := b.build()
	resource, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
	if err != nil {
		return fmt.Errorf("failed to convert ConfigMap to unstructured: %w", err)
	}
	cfgMapBytes, err := yaml.Marshal(resource)
	if err != nil {
		return fmt.Errorf("failed to marshal ConfigMap: %w", err)
	}
	k8s.KubectlApplyFromString(t, kubeOpts, string(cfgMapBytes))
	return nil
}

// JSONPatchBuilder provides a fluent interface for building JSON patches.
type JSONPatchBuilder struct {
	operations []patchOperation
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// NewJSONPatchBuilder creates a new JSONPatchBuilder.
func NewJSONPatchBuilder() *JSONPatchBuilder {
	return &JSONPatchBuilder{
		operations: make([]patchOperation, 0),
	}
}

func (b *JSONPatchBuilder) Add(path string, value interface{}) *JSONPatchBuilder {
	b.operations = append(b.operations, patchOperation{
		Op:    "add",
		Path:  path,
		Value: value,
	})
	return b
}

func (b *JSONPatchBuilder) Replace(path string, value interface{}) *JSONPatchBuilder {
	b.operations = append(b.operations, patchOperation{
		Op:    "replace",
		Path:  path,
		Value: value,
	})
	return b
}

// WithVMSingleConfig configures VMSingle with a ConfigMap for the given extra arg.
func (b *JSONPatchBuilder) WithVMSingleConfig(cfgMapName, extraArgKey, configFileName string) *JSONPatchBuilder {
	configPath := fmt.Sprintf("/etc/vm/configs/%s/%s", cfgMapName, configFileName)
	// Ensure parent paths exist before adding child entries to avoid
	// "doc is missing path" errors when applying the JSON patch.
	return b.
		Add("/spec/extraArgs", map[string]string{}).
		Add("/spec/extraArgs/"+extraArgKey, configPath).
		Add("/spec/configMaps", []string{}).
		Add("/spec/configMaps/-", cfgMapName)
}

// WithExtraArg adds an extra argument to the VMSingle configuration.
func (b *JSONPatchBuilder) WithExtraArg(key, value string) *JSONPatchBuilder {
	return b.
		Add("/spec/extraArgs", map[string]string{}).
		Add("/spec/extraArgs/"+key, value)
}

func (b *JSONPatchBuilder) build() (jsonpatch.Patch, error) {
	patchBytes, err := json.Marshal(b.operations)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal patch operations: %w", err)
	}
	return jsonpatch.DecodePatch(patchBytes)
}

// MustBuild creates the JSON patch, panicking on error.
func (b *JSONPatchBuilder) MustBuild() jsonpatch.Patch {
	patch, err := b.build()
	if err != nil {
		panic(err)
	}
	return patch
}

// PromClientBuilder provides a fluent interface for building Prometheus clients.
type PromClientBuilder struct {
	baseURL   string
	tenantID  *int
	namespace string
	startTime time.Time
	timeout   time.Duration
}

// NewPromClientBuilder creates a new PromClientBuilder.
func NewPromClientBuilder() *PromClientBuilder {
	return &PromClientBuilder{
		timeout: consts.HTTPClientTimeout,
	}
}

// WithBaseURL sets the base URL for the Prometheus client.
func (b *PromClientBuilder) WithBaseURL(url string) *PromClientBuilder {
	b.baseURL = url
	return b
}

// WithNamespace sets the namespace for URL construction.
func (b *PromClientBuilder) WithNamespace(namespace string) *PromClientBuilder {
	b.namespace = namespace
	return b
}

// WithTenant sets the tenant ID for URL construction.
func (b *PromClientBuilder) WithTenant(tenantID int) *PromClientBuilder {
	b.tenantID = &tenantID
	return b
}

// Multitenant configures the client for multitenant access.
func (b *PromClientBuilder) Multitenant() *PromClientBuilder {
	b.tenantID = nil
	return b
}

// WithStartTime sets the start time for the client.
func (b *PromClientBuilder) WithStartTime(t time.Time) *PromClientBuilder {
	b.startTime = t
	return b
}

// ForVMSingle configures the client for a VMSingle instance in the given namespace.
func (b *PromClientBuilder) ForVMSingle(namespace string) *PromClientBuilder {
	b.baseURL = VMSinglePrometheusURL(namespace)
	return b
}

func (b *PromClientBuilder) build() (promquery.PrometheusClient, error) {
	url := b.baseURL
	if url == "" && b.namespace != "" {
		if b.tenantID != nil {
			url = TenantSelectURL(b.namespace, *b.tenantID)
		} else {
			url = MultitenantSelectURL(b.namespace)
		}
	}

	if url == "" {
		return promquery.PrometheusClient{}, fmt.Errorf("no URL configured for Prometheus client")
	}

	client, err := promquery.NewPrometheusClient(url)
	if err != nil {
		return promquery.PrometheusClient{}, err
	}

	if !b.startTime.IsZero() {
		client.Start = b.startTime
	}

	return client, nil
}

// MustBuild creates the Prometheus client, panicking on error.
func (b *PromClientBuilder) MustBuild() promquery.PrometheusClient {
	client, err := b.build()
	if err != nil {
		panic(err)
	}
	return client
}

// TimeSeriesBuilder provides a fluent interface for building time series data.
type TimeSeriesBuilder struct {
	prefix     string
	count      int
	value      float64
	labels     map[string]string
	httpClient *http.Client
}

// NewTimeSeriesBuilder creates a new TimeSeriesBuilder.
func NewTimeSeriesBuilder(prefix string) *TimeSeriesBuilder {
	return &TimeSeriesBuilder{
		prefix:     prefix,
		count:      10,
		value:      1,
		labels:     make(map[string]string),
		httpClient: NewHTTPClient(),
	}
}

// WithCount sets the number of time series to generate.
func (b *TimeSeriesBuilder) WithCount(count int) *TimeSeriesBuilder {
	b.count = count
	return b
}

// WithValue sets the value for the time series.
func (b *TimeSeriesBuilder) WithValue(value float64) *TimeSeriesBuilder {
	b.value = value
	return b
}

// WithLabel adds a custom label to the time series.
func (b *TimeSeriesBuilder) WithLabel(name, value string) *TimeSeriesBuilder {
	b.labels[name] = value
	return b
}

// WithTenantLabel adds a vm_account_id label for multitenant writes.
func (b *TimeSeriesBuilder) WithTenantLabel(tenantID int) *TimeSeriesBuilder {
	b.labels["vm_account_id"] = fmt.Sprintf("%d", tenantID)
	return b
}

// WithHTTPClient sets a custom HTTP client.
func (b *TimeSeriesBuilder) WithHTTPClient(client *http.Client) *TimeSeriesBuilder {
	b.httpClient = client
	return b
}

// Build generates the time series data.
func (b *TimeSeriesBuilder) Build() []prompb.TimeSeries {
	ts := remotewrite.GenTimeSeries(b.prefix, b.count, b.value)

	// Add custom labels if any
	if len(b.labels) > 0 {
		for i := range ts {
			for k, v := range b.labels {
				ts[i].Labels = append(ts[i].Labels, prompb.Label{
					Name:  k,
					Value: v,
				})
			}
		}
	}

	return ts
}

// Send generates and sends the time series to the specified URL.
func (b *TimeSeriesBuilder) Send(ctx context.Context, url string) error {

	ts := b.Build()
	return remotewrite.RemoteWrite(b.httpClient, ts, url)
}

// RemoteWriteBuilder provides a fluent interface for remote write operations.
type RemoteWriteBuilder struct {
	httpClient *http.Client
	url        string
}

// NewRemoteWriteBuilder creates a new RemoteWriteBuilder.
func NewRemoteWriteBuilder() *RemoteWriteBuilder {
	return &RemoteWriteBuilder{
		httpClient: NewHTTPClient(),
	}
}

// WithHTTPClient sets a custom HTTP client.
func (b *RemoteWriteBuilder) WithHTTPClient(client *http.Client) *RemoteWriteBuilder {
	b.httpClient = client
	return b
}

// WithURL sets the remote write URL directly.
func (b *RemoteWriteBuilder) WithURL(url string) *RemoteWriteBuilder {
	b.url = url
	return b
}

// ForVMSingle configures the builder for a VMSingle instance.
func (b *RemoteWriteBuilder) ForVMSingle(namespace string) *RemoteWriteBuilder {
	b.url = VMSingleRemoteWriteURL(namespace)
	return b
}

// ForMultitenant configures the builder for multitenant writes.
func (b *RemoteWriteBuilder) ForMultitenant(namespace string) *RemoteWriteBuilder {
	b.url = MultitenantInsertURL(namespace)
	return b
}

// Send sends the time series to the configured URL.
func (b *RemoteWriteBuilder) Send(ts []prompb.TimeSeries) error {
	if b.url == "" {
		return fmt.Errorf("no URL configured for remote write")
	}
	return remotewrite.RemoteWrite(b.httpClient, ts, b.url)
}

// ForVMAgent configures the builder for a VMAgent instance.
func (b *RemoteWriteBuilder) ForVMAgent(namespace string) *RemoteWriteBuilder {
	b.url = VMAgentRemoteWriteURL(namespace)
	return b
}

// ForTenant configures the builder for a specific tenant.
func (b *RemoteWriteBuilder) ForTenant(namespace string, tenantID int) *RemoteWriteBuilder {
	b.url = TenantInsertURL(namespace, tenantID)
	return b
}

// RelabelConfigBuilder provides a fluent interface for building relabel configurations.
type RelabelConfigBuilder struct {
	rules []relabelRule
}

// We need to have both yaml and json tags so that yaml.Marshal would retain the case, as it does both yaml and json marshaling.
type relabelRule struct {
	TargetLabel  string   `yaml:"target_label,omitempty" json:"target_label,omitempty"`
	Replacement  string   `yaml:"replacement,omitempty" json:"replacement,omitempty"`
	Action       string   `yaml:"action,omitempty" json:"action,omitempty"`
	SourceLabels []string `yaml:"source_labels,omitempty" json:"source_labels,omitempty"`
	Regex        string   `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// NewRelabelConfigBuilder creates a new RelabelConfigBuilder.
func NewRelabelConfigBuilder() *RelabelConfigBuilder {
	return &RelabelConfigBuilder{
		rules: make([]relabelRule, 0),
	}
}

// AddLabel adds a rule to set a label to a fixed value.
func (b *RelabelConfigBuilder) AddLabel(targetLabel, replacement string) *RelabelConfigBuilder {
	b.rules = append(b.rules, relabelRule{
		TargetLabel: targetLabel,
		Replacement: replacement,
	})
	return b
}

// DropByName adds a rule to drop metrics matching a name regex.
func (b *RelabelConfigBuilder) DropByName(regex string) *RelabelConfigBuilder {
	b.rules = append(b.rules, relabelRule{
		Action:       "drop",
		SourceLabels: []string{"__name__"},
		Regex:        regex,
	})
	return b
}

func (b *RelabelConfigBuilder) build() (string, error) {
	data, err := yaml.Marshal(b.rules)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DatadogSeries represents the top-level structure for Datadog ingestion.
type DatadogSeries struct {
	Series []DatadogMetric `json:"series"`
}

// DatadogMetric represents a single metric in Datadog format.
type DatadogMetric struct {
	Metric string          `json:"metric"`
	Points [][]interface{} `json:"points"`
	Tags   []string        `json:"tags,omitempty"`
	Host   string          `json:"host,omitempty"`
	Type   string          `json:"type,omitempty"`
}

// MustBuild generates the YAML configuration, panicking on error.
func (b *RelabelConfigBuilder) MustBuild() string {
	config, err := b.build()
	if err != nil {
		panic(err)
	}
	return config
}

// StreamAggrConfigBuilder provides a fluent interface for building streaming aggregation configs.
type StreamAggrConfigBuilder struct {
	rules []streamAggrRule
}

// We need to have both yaml and json tags so that yaml.Marshal would retain the case, as it does both yaml and json marshaling.
type streamAggrRule struct {
	Match    string   `yaml:"match" json:"match"`
	Interval string   `yaml:"interval" json:"interval"`
	Outputs  []string `yaml:"outputs" json:"outputs"`
	Without  []string `yaml:"without,omitempty" json:"without,omitempty"`
	By       []string `yaml:"by,omitempty" json:"by,omitempty"`
}

// NewStreamAggrConfigBuilder creates a new StreamAggrConfigBuilder.
func NewStreamAggrConfigBuilder() *StreamAggrConfigBuilder {
	return &StreamAggrConfigBuilder{
		rules: make([]streamAggrRule, 0),
	}
}

// AddRule adds a streaming aggregation rule.
func (b *StreamAggrConfigBuilder) AddRule(match, interval string, outputs []string) *StreamAggrConfigBuilder {
	b.rules = append(b.rules, streamAggrRule{
		Match:    match,
		Interval: interval,
		Outputs:  outputs,
	})
	return b
}

// WithoutLabels sets labels to exclude from aggregation for the last rule.
func (b *StreamAggrConfigBuilder) WithoutLabels(labels ...string) *StreamAggrConfigBuilder {
	if len(b.rules) > 0 {
		b.rules[len(b.rules)-1].Without = labels
	}
	return b
}

func (b *StreamAggrConfigBuilder) build() (string, error) {
	data, err := yaml.Marshal(b.rules)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// MustBuild generates the YAML configuration, panicking on error.
func (b *StreamAggrConfigBuilder) MustBuild() string {
	config, err := b.build()
	if err != nil {
		panic(err)
	}
	return config
}
