package consts

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// PollingInterval is the interval at which tests verify conditions (e.g. resource readiness).
	PollingInterval = 5 * time.Second
	// PollingTimeout defines the overall timeout for polling operations.
	PollingTimeout = 15 * time.Minute
	// ResourceWaitTimeout is the maximum duration to wait for Kubernetes resources to become available.
	ResourceWaitTimeout = 5 * time.Minute
	// VMClusterWaitTimeout is the maximum duration to wait for a VMCluster to become operational.
	// Longer than ResourceWaitTimeout to account for node autoscaler provisioning delays.
	VMClusterWaitTimeout = 10 * time.Minute

	// K6JobPollingInterval is the interval for checking K6 job status.
	K6JobPollingInterval = 1 * time.Minute

	// K6JobMaxDuration is the maximum allowed duration for a K6 load test job.
	K6JobMaxDuration = 20 * time.Minute

	// ChaosTestMaxDuration is the maximum allowed duration for a Chaos Mesh scenario.
	ChaosTestMaxDuration = 30 * time.Minute

	// HTTPClientTimeout is the default timeout for HTTP clients used in tests.
	HTTPClientTimeout = 10 * time.Second

	// DataPropagationDelay is the time to wait for data to propagate through the system.
	DataPropagationDelay = 30 * time.Second

	// AggregationWaitTime is the time to wait for streaming aggregation to complete.
	AggregationWaitTime = 1 * time.Minute

	// VMStorageCycleInterval is the minimum stable window between consecutive
	// vmstorage resource-cycling disruptions in load tests.
	VMStorageCycleInterval = 90 * time.Second
)

// Common namespace constants used across tests.
const (
	// DefaultVMNamespace is the default namespace for VictoriaMetrics deployments.
	DefaultVMNamespace = "monitoring"

	// OverwatchNamespace is the namespace for the overwatch monitoring stack.
	OverwatchNamespace = "overwatch"

	// K6OperatorNamespace is the namespace for the k6 operator.
	K6OperatorNamespace = "k6-operator-system"

	// LoadTestVMNamespace is the dedicated namespace for the VMCluster used by load tests.
	// The cluster is named after the namespace, following the same convention as chaos_tests.
	LoadTestVMNamespace = "vm-load-test"

	// ChaosMeshNamespace is the namespace for chaos mesh.
	ChaosMeshNamespace = "chaos-mesh"

	// KafkaNamespace is the namespace for the Strimzi Kafka operator.
	KafkaNamespace = "kafka"

	// KEDANamespace is the namespace for the KEDA operator.
	KEDANamespace = "keda"
)

// MDX remote write configuration for central monitoring.
const (
	// MDXRemoteWriteURL is the remote write endpoint for the central monitoring system.
	MDXRemoteWriteURL = "https://maas.victoriametrics.com/metrics/insert/prometheus/api/v1/write"

	// MDXRemoteWriteUsername is the username for basic auth to the central monitoring system.
	MDXRemoteWriteUsername = "monitoring-5"

	// MDXRemoteWriteSecretName is the name of the K8s Secret holding MDX remote write credentials.
	MDXRemoteWriteSecretName = "mdx-remote-write-secret"
)

// Common release and resource names used across tests.
const (
	// DefaultReleaseName is the default Helm release name for VM k8s stack.
	DefaultReleaseName = "vmks"

	// DefaultVMClusterName is the default name for VMCluster resources.
	DefaultVMClusterName = "vm"

	// ChaosMeshReleaseName is the Helm release name for chaos mesh.
	ChaosMeshReleaseName = "chaos-mesh"

	// KEDAReleaseName is the Helm release name for KEDA.
	KEDAReleaseName = "keda"

	// DefaultVLReleaseName is the default Helm release name for VictoriaLogs single.
	DefaultVLReleaseName = "vlks"

	// DefaultVLCollectorReleaseName is the default Helm release name for VictoriaLogs Collector.
	DefaultVLCollectorReleaseName = "vlogs-collector"
)

// Helm chart references.
const (
	// VMK8sStackChart is the Helm chart for VictoriaMetrics k8s stack.
	VMK8sStackChart = "vm/victoria-metrics-k8s-stack"

	// VMDistributedChart is the Helm chart for VictoriaMetrics distributed deployment.
	VMDistributedChart = "vm/victoria-metrics-distributed"

	// ChaosMeshChart is the Helm chart for Chaos Mesh.
	ChaosMeshChart = "chaos-mesh/chaos-mesh"

	// KEDAChart is the Helm chart for KEDA.
	KEDAChart = "kedacore/keda"

	// VictoriaLogsSingleChart is the Helm chart for VictoriaLogs single-node.
	VictoriaLogsSingleChart = "vm/victoria-logs-single"

	// VictoriaLogsCollectorChart is the Helm chart for VictoriaLogs Collector (k8s pod log collector).
	VictoriaLogsCollectorChart = "vm/victoria-logs-collector"
)

// Values file paths (relative to test directories).
const (
	// LicenseSecretName is the name of the secret containing the license key.
	LicenseSecretName = "vm-license"

	// LicenseSecretKey is the key in the secret containing the license key.
	LicenseSecretKey = "key"
)

// Common error messages.
const (
	// ErrNoDataReturned is the error message when a query returns no data.
	ErrNoDataReturned = "no data returned"
)

// URL path patterns for VictoriaMetrics endpoints.
const (
	// PrometheusPathSuffix is the suffix for Prometheus-compatible endpoints.
	PrometheusPathSuffix = "/prometheus"

	// TenantInsertPathFormat is the format for tenant-specific insert URLs.
	// Arguments: tenant ID
	TenantInsertPathFormat = "/insert/%d/prometheus/api/v1/write"

	// TenantImportPathFormat is the format for tenant-specific Prometheus text/plain import URLs.
	// Arguments: tenant ID
	TenantImportPathFormat = "/insert/%d/prometheus/api/v1/import/prometheus"

	// TenantSelectPathFormat is the format for tenant-specific select URLs.
	// Arguments: tenant ID
	TenantSelectPathFormat = "/select/%d/prometheus"

	// MultitenantInsertPath is the path for multitenant insert endpoint.
	MultitenantInsertPath = "/insert/multitenant/prometheus/api/v1/write"

	// MultitenantSelectPath is the path for multitenant select endpoint.
	MultitenantSelectPath = "/select/multitenant/prometheus"

	// RemoteWritePath is the path for remote write API.
	RemoteWritePath = "/api/v1/write"

	// ImportPrometheusPath is the path for prometheus text format import API.
	ImportPrometheusPath = "/api/v1/import/prometheus"
)

var (
	// Retries is the number of attempts to make based on ResourceWaitTimeout and PollingInterval.
	Retries = int(ResourceWaitTimeout.Seconds() / PollingInterval.Seconds())
	// K6Retries is the number of attempts for K6 jobs based on K6JobMaxDuration.
	K6Retries = int(K6JobMaxDuration.Seconds() / K6JobPollingInterval.Seconds())
)

var (
	mu sync.Mutex

	manifestsDir string

	reportLocation string
	envK8SDistro   string

	nginxHost string

	helmChartVersion string
	operatorVersion  string
	vmVersion        string

	vmK8sStackChartVersion       string
	vmDistributedChartVersion    string
	vlSingleChartVersion         string
	vlCollectorChartVersion      string

	operatorImageRegistry   string
	operatorImageRepository string
	operatorImageTag        string

	vmSingleDefaultImage   string
	vmSingleDefaultVersion string

	vmClusterVMSelectDefaultImage   string
	vmClusterVMSelectDefaultVersion string

	vmClusterVMStorageDefaultImage   string
	vmClusterVMStorageDefaultVersion string

	vmClusterVMInsertDefaultImage   string
	vmClusterVMInsertDefaultVersion string

	vmAgentDefaultImage   string
	vmAgentDefaultVersion string

	vmAlertDefaultImage   string
	vmAlertDefaultVersion string

	vmAuthDefaultImage   string
	vmAuthDefaultVersion string

	vmBackupDefaultImage   string
	vmBackupDefaultVersion string

	vmRestoreDefaultImage   string
	vmRestoreDefaultVersion string
	licenseFile             string
	distributedRegion       string
	distributedZones        string
)

// Setters

// SetManifestsDir overrides the base path for manifest files.
func SetManifestsDir(val string) {
	mu.Lock()
	defer mu.Unlock()
	manifestsDir = val
}

// ManifestsRoot returns the base path for manifest files.
func ManifestsRoot() string {
	mu.Lock()
	defer mu.Unlock()
	if manifestsDir != "" {
		return manifestsDir
	}
	return "../../manifests"
}

// OverwatchVMAgentYaml returns the path to the overwatch VMAgent manifest.
func OverwatchVMAgentYaml() string { return ManifestsRoot() + "/overwatch/vmagent.yaml" }

// OverwatchVMSingleIngress returns the path to the overwatch VMSingle ingress manifest.
func OverwatchVMSingleIngress() string { return ManifestsRoot() + "/overwatch/vmsingle-ingress.yaml" }

// SmokeValuesFile returns the values file path for smoke tests.
func SmokeValuesFile() string { return ManifestsRoot() + "/smoke.yaml" }

// DistributedValuesFile returns the values file path for distributed chart tests.
func DistributedValuesFile() string { return ManifestsRoot() + "/distributed.yaml" }

// ChaosMeshValuesFile returns the values file path for chaos mesh.
func ChaosMeshValuesFile() string { return ManifestsRoot() + "/chaos-mesh-operator/values.yaml" }

// KEDAValuesFile returns the values file path for KEDA.
func KEDAValuesFile() string { return ManifestsRoot() + "/keda/values.yaml" }

// VictoriaLogsSingleValuesFile returns the values file path for VictoriaLogs single.
func VictoriaLogsSingleValuesFile() string { return ManifestsRoot() + "/victoria-logs.yaml" }

// VictoriaLogsCollectorValuesFile returns the values file path for VictoriaLogs Collector.
func VictoriaLogsCollectorValuesFile() string { return ManifestsRoot() + "/victoria-logs-collector.yaml" }

// SetReportLocation sets the path for test reports.
func SetReportLocation(val string) {
	mu.Lock()
	defer mu.Unlock()
	reportLocation = val
}

// SetEnvK8SDistro sets the Kubernetes distribution name (e.g., kind, gke).
func SetEnvK8SDistro(val string) {
	mu.Lock()
	defer mu.Unlock()
	envK8SDistro = val
}

// SetNginxHost sets the external hostname for Nginx ingress.
func SetNginxHost(val string) {
	mu.Lock()
	defer mu.Unlock()
	nginxHost = val
}

// SetHelmChartVersion sets the detected Helm chart version.
func SetHelmChartVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	helmChartVersion = val
}

// SetVMK8sStackChartVersion sets the desired install version for the victoria-metrics-k8s-stack chart.
func SetVMK8sStackChartVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmK8sStackChartVersion = val
}

// VMK8sStackChartVersion returns the desired install version for the victoria-metrics-k8s-stack chart.
func VMK8sStackChartVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmK8sStackChartVersion
}

// SetVMDistributedChartVersion sets the desired install version for the victoria-metrics-distributed chart.
func SetVMDistributedChartVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmDistributedChartVersion = val
}

// VMDistributedChartVersion returns the desired install version for the victoria-metrics-distributed chart.
func VMDistributedChartVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmDistributedChartVersion
}

// SetVLSingleChartVersion sets the desired install version for the victoria-logs-single chart.
func SetVLSingleChartVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vlSingleChartVersion = val
}

// VLSingleChartVersion returns the desired install version for the victoria-logs-single chart.
func VLSingleChartVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vlSingleChartVersion
}

// SetVLCollectorChartVersion sets the desired install version for the victoria-logs-collector chart.
func SetVLCollectorChartVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vlCollectorChartVersion = val
}

// VLCollectorChartVersion returns the desired install version for the victoria-logs-collector chart.
func VLCollectorChartVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vlCollectorChartVersion
}

// SetOperatorVersion sets the detected VictoriaMetrics Operator version.
func SetOperatorVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	operatorVersion = val
}

// SetVMVersion sets the detected VictoriaMetrics Operator version.
func SetVMVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmVersion = val
}

// SetOperatorImageRegistry sets the operator image registry.
func SetOperatorImageRegistry(val string) {
	mu.Lock()
	defer mu.Unlock()
	operatorImageRegistry = val
}

// SetOperatorImageRepository sets the operator image repository.
func SetOperatorImageRepository(val string) {
	mu.Lock()
	defer mu.Unlock()
	operatorImageRepository = val
}

// SetOperatorImageTag sets the operator image tag.
func SetOperatorImageTag(val string) {
	mu.Lock()
	defer mu.Unlock()
	operatorImageTag = val
}

// SetVMSingleDefaultImage sets the default image for VMSingle.
func SetVMSingleDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmSingleDefaultImage = val
}

// SetVMSingleDefaultVersion sets the default version for VMSingle.
func SetVMSingleDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmSingleDefaultVersion = val
}

// SetVMClusterVMSelectDefaultImage sets the default image for VMCluster VMSelect.
func SetVMClusterVMSelectDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmClusterVMSelectDefaultImage = val
}

// SetVMClusterVMSelectDefaultVersion sets the default version for VMCluster VMSelect.
func SetVMClusterVMSelectDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmClusterVMSelectDefaultVersion = val
}

// SetVMClusterVMStorageDefaultImage sets the default image for VMCluster VMStorage.
func SetVMClusterVMStorageDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmClusterVMStorageDefaultImage = val
}

// SetVMClusterVMStorageDefaultVersion sets the default version for VMCluster VMStorage.
func SetVMClusterVMStorageDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmClusterVMStorageDefaultVersion = val
}

// SetVMClusterVMInsertDefaultImage sets the default image for VMCluster VMInsert.
func SetVMClusterVMInsertDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmClusterVMInsertDefaultImage = val
}

// SetVMClusterVMInsertDefaultVersion sets the default version for VMCluster VMInsert.
func SetVMClusterVMInsertDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmClusterVMInsertDefaultVersion = val
}

// SetVMAgentDefaultImage sets the default image for VMAgent.
func SetVMAgentDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmAgentDefaultImage = val
}

// SetVMAgentDefaultVersion sets the default version for VMAgent.
func SetVMAgentDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmAgentDefaultVersion = val
}

// SetVMAlertDefaultImage sets the default image for VMAlert.
func SetVMAlertDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmAlertDefaultImage = val
}

// SetVMAlertDefaultVersion sets the default version for VMAlert.
func SetVMAlertDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmAlertDefaultVersion = val
}

// SetVMAuthDefaultImage sets the default image for VMAuth.
func SetVMAuthDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmAuthDefaultImage = val
}

// SetVMAuthDefaultVersion sets the default version for VMAuth.
func SetVMAuthDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmAuthDefaultVersion = val
}

// SetVMBackupDefaultImage sets the default image for VMBackup.
func SetVMBackupDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmBackupDefaultImage = val
}

// SetVMBackupDefaultVersion sets the default version for VMBackup.
func SetVMBackupDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmBackupDefaultVersion = val
}

// SetVMRestoreDefaultImage sets the default image for VMRestore.
func SetVMRestoreDefaultImage(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmRestoreDefaultImage = val
}

// SetVMRestoreDefaultVersion sets the default version for VMRestore.
func SetVMRestoreDefaultVersion(val string) {
	mu.Lock()
	defer mu.Unlock()
	vmRestoreDefaultVersion = val
}

// SetLicenseFile sets the license file path.
func SetLicenseFile(val string) {
	mu.Lock()
	defer mu.Unlock()
	licenseFile = val
}

// Getters

// ReportLocation returns the configured report location.
func SetDistributedRegion(region string) {
	mu.Lock()
	defer mu.Unlock()
	distributedRegion = region
}

func SetDistributedZones(zones string) {
	mu.Lock()
	defer mu.Unlock()
	distributedZones = zones
}

func ReportLocation() string {
	mu.Lock()
	defer mu.Unlock()
	return reportLocation
}

// EnvK8SDistro returns the configured Kubernetes distribution.
func EnvK8SDistro() string {
	mu.Lock()
	defer mu.Unlock()
	return envK8SDistro
}

// NginxHost returns the configured Nginx host.
func NginxHost() string {
	mu.Lock()
	defer mu.Unlock()
	return nginxHost
}

// VMSingleUrl constructs the URL for the VMSingle instance.
func VMSingleUrl() string {
	return fmt.Sprintf("http://%s", VMSingleHost())
}

// VMSelectUrl constructs the URL for the VMSelect instance in the given namespace.
func VMSelectUrl(namespace string) string {
	return fmt.Sprintf("http://%s", VMSelectHost(namespace))
}

// VMSingleHost returns the hostname for VMSingle.
func VMSingleHost() string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmsingle.%s.nip.io", host)
}

// VMSingleNamespacedHost returns the hostname for VMSingle in the given namespace.
func VMSingleNamespacedHost(namespace string) string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmsingle-%s.%s.nip.io", namespace, host)
}

// VMAgentNamespacedHost returns the hostname for VMAgent in the given namespace.
func VMAgentNamespacedHost(namespace string) string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmagent-%s.%s.nip.io", namespace, host)
}

// VMAgentNamedHost returns the hostname for a named VMAgent in the given namespace.
// Use this for VMAgents whose CR name differs from "vmagent".
func VMAgentNamedHost(name, namespace string) string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s-%s.%s.nip.io", name, namespace, host)
}

// VMSelectHost returns the hostname for VMSelect in the given namespace.
func VMSelectHost(namespace string) string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	if namespace == "" {
		return fmt.Sprintf("vmselect.%s.nip.io", host)
	}
	return fmt.Sprintf("vmselect-%s.%s.nip.io", namespace, host)
}

// VMInsertHost returns the hostname for VMInsert in the given namespace.
func VMInsertHost(namespace string) string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	if namespace == "" {
		return fmt.Sprintf("vminsert.%s.nip.io", host)
	}
	return fmt.Sprintf("vminsert-%s.%s.nip.io", namespace, host)
}

// VMAuthHost returns the hostname for the VMAuth created by VMDistributed in the given namespace.
func VMAuthHost(namespace string) string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmauth-%s.%s.nip.io", namespace, host)
}

// AlertManagerHost returns the hostname for AlertManager in the given namespace.
func AlertManagerHost(namespace string) string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	if namespace == "" {
		return fmt.Sprintf("alert.%s.nip.io", host)
	}
	return fmt.Sprintf("alert-%s.%s.nip.io", namespace, host)
}

// VLHost returns the ingress hostname for VictoriaLogs single.
func VLHost() string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vl.%s.nip.io", host)
}

// VMGatherHost returns the hostname for VMGather.
func VMGatherHost() string {
	mu.Lock()
	host := nginxHost
	mu.Unlock()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmgather.%s.nip.io", host)
}

// Kubernetes service address functions

// GetVMSelectSvc returns the internal Kubernetes service address for VMSelect.
func GetVMSelectSvc(releaseName, namespace string) string {
	return fmt.Sprintf("vmselect-%s.%s.svc.cluster.local:8481", releaseName, namespace)
}

// GetVMSingleSvc returns the internal Kubernetes service address for VMSingle.
func GetVMSingleSvc(releaseName, namespace string) string {
	return fmt.Sprintf("vmsingle-%s.%s.svc.cluster.local:8428", releaseName, namespace)
}

// GetVMInsertSvc returns the internal Kubernetes service address for VMInsert.
func GetVMInsertSvc(releaseName, namespace string) string {
	return fmt.Sprintf("vminsert-%s.%s.svc.cluster.local:8480", releaseName, namespace)
}

// GetVLSingleSvc returns the internal Kubernetes service address for VictoriaLogs single.
func GetVLSingleSvc(releaseName, namespace string) string {
	return fmt.Sprintf("%s-victoria-logs-single-server.%s.svc.cluster.local:9428", releaseName, namespace)
}

// KafkaBrokerSvc returns the in-cluster bootstrap address for the Strimzi Kafka cluster
// deployed in the given namespace by install.InstallKafka.
func KafkaBrokerSvc(namespace string) string {
	return fmt.Sprintf("kafka-kafka-bootstrap.%s.svc.cluster.local:9092", namespace)
}

// HelmChartVersion returns the stored Helm chart version.
func HelmChartVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return helmChartVersion
}

// OperatorVersion returns the stored Operator version.
func OperatorVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return operatorVersion
}

// VMVersion returns the stored Operator version.
func VMVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmVersion
}

// OperatorImageRegistry returns the stored operator image registry.
func OperatorImageRegistry() string {
	mu.Lock()
	defer mu.Unlock()
	return operatorImageRegistry
}

// OperatorImageRepository returns the stored operator image repository.
func OperatorImageRepository() string {
	mu.Lock()
	defer mu.Unlock()
	return operatorImageRepository
}

// OperatorImageTag returns the stored operator image tag.
func OperatorImageTag() string {
	mu.Lock()
	defer mu.Unlock()
	return operatorImageTag
}

// VMSingleDefaultImage returns the stored VMSingle default image.
func VMSingleDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmSingleDefaultImage
}

// VMSingleDefaultVersion returns the stored VMSingle default version.
func VMSingleDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmSingleDefaultVersion
}

// VMClusterVMSelectDefaultImage returns the stored VMCluster VMSelect default image.
func VMClusterVMSelectDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmClusterVMSelectDefaultImage
}

// VMClusterVMSelectDefaultVersion returns the stored VMCluster VMSelect default version.
func VMClusterVMSelectDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmClusterVMSelectDefaultVersion
}

// VMClusterVMStorageDefaultImage returns the stored VMCluster VMStorage default image.
func VMClusterVMStorageDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmClusterVMStorageDefaultImage
}

// VMClusterVMStorageDefaultVersion returns the stored VMCluster VMStorage default version.
func VMClusterVMStorageDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmClusterVMStorageDefaultVersion
}

// VMClusterVMInsertDefaultImage returns the stored VMCluster VMInsert default image.
func VMClusterVMInsertDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmClusterVMInsertDefaultImage
}

// VMClusterVMInsertDefaultVersion returns the stored VMCluster VMInsert default version.
func VMClusterVMInsertDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmClusterVMInsertDefaultVersion
}

// VMAgentDefaultImage returns the stored VMAgent default image.
func VMAgentDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmAgentDefaultImage
}

// VMAgentDefaultVersion returns the stored VMAgent default version.
func VMAgentDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmAgentDefaultVersion
}

// VMAlertDefaultImage returns the stored VMAlert default image.
func VMAlertDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmAlertDefaultImage
}

// VMAlertDefaultVersion returns the stored VMAlert default version.
func VMAlertDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmAlertDefaultVersion
}

// VMAuthDefaultImage returns the stored VMAuth default image.
func VMAuthDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmAuthDefaultImage
}

// VMAuthDefaultVersion returns the stored VMAuth default version.
func VMAuthDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmAuthDefaultVersion
}

// VMBackupDefaultImage returns the stored VMBackup default image.
func VMBackupDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmBackupDefaultImage
}

// VMBackupDefaultVersion returns the stored VMBackup default version.
func VMBackupDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmBackupDefaultVersion
}

// VMRestoreDefaultImage returns the stored VMRestore default image.
func VMRestoreDefaultImage() string {
	mu.Lock()
	defer mu.Unlock()
	return vmRestoreDefaultImage
}

// VMRestoreDefaultVersion returns the stored VMRestore default version.
func VMRestoreDefaultVersion() string {
	mu.Lock()
	defer mu.Unlock()
	return vmRestoreDefaultVersion
}

// LicenseFile returns the stored license file path.
func LicenseFile() string {
	mu.Lock()
	defer mu.Unlock()
	return licenseFile
}

func DistributedRegion() string {
	mu.Lock()
	defer mu.Unlock()
	return distributedRegion
}

func DistributedZones() string {
	mu.Lock()
	defer mu.Unlock()
	return distributedZones
}

// PrepareLicenseSecret creates a Secret manifest for the license key.
func PrepareLicenseSecret(namespace string) (string, error) {
	if LicenseFile() == "" {
		return "", nil
	}
	licenseKey, err := os.ReadFile(LicenseFile())
	if err != nil {
		return "", fmt.Errorf("failed to read license file: %w", err)
	}

	secretYaml := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
stringData:
  %s: %q
`, LicenseSecretName, namespace, LicenseSecretKey, strings.TrimSpace(string(licenseKey)))
	return secretYaml, nil
}
