package consts

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const gatewayAPIStandardVersion = "v1.6.1"

const (
	// PollingInterval is the interval at which tests verify conditions (e.g. resource readiness).
	PollingInterval = 5 * time.Second
	// PollingTimeout defines the overall timeout for polling operations.
	PollingTimeout = 15 * time.Minute
	// ResourceWaitTimeout is the maximum duration to wait for Kubernetes resources to become available.
	ResourceWaitTimeout = 3 * time.Minute
	// VMClusterWaitTimeout is the maximum duration to wait for a VMCluster to become operational.
	// Longer than ResourceWaitTimeout to account for node autoscaler provisioning delays.
	VMClusterWaitTimeout = 5 * time.Minute

	// K6JobPollingInterval is the interval for checking K6 job status.
	K6JobPollingInterval = 1 * time.Minute

	// K6JobMaxDuration is the maximum allowed duration for a K6 load test job.
	K6JobMaxDuration = 20 * time.Minute

	// ChaosTestMaxDuration is the maximum allowed duration for a Chaos Mesh scenario.
	ChaosTestMaxDuration = 30 * time.Minute
	// ChaosSpecTimeout is the maximum duration for a single chaos test spec, covering
	// VMCluster setup (PollingTimeout), chaos execution (ChaosTestMaxDuration), and DeferCleanup teardown.
	ChaosSpecTimeout = PollingTimeout + ChaosTestMaxDuration

	// VMFunctionalSpecTimeout is the maximum duration for a single VM functional test spec.
	// Sized to cover the heaviest spec (VMSingle backup/restore, which chains three
	// InstallVMSingle calls) with buffer, so a stuck install fails fast with a clear
	// timeout instead of hanging until the suite-wide --timeout is exhausted.
	VMFunctionalSpecTimeout = 30 * time.Minute

	// VLFunctionalSpecTimeout is the maximum duration for a single VL functional test spec.
	// Sized to cover the heaviest spec (VLCollector, which chains a VLSingle install and a
	// collector install) with buffer, for the same reason as VMFunctionalSpecTimeout.
	VLFunctionalSpecTimeout = 30 * time.Minute

	// VMEnterpriseSpecTimeout is the maximum duration for a single VM enterprise test spec.
	// Sized to cover the heaviest spec (mTLS, which installs a VMCluster with PollingTimeout
	// then exposes two VMAgents as ingress) with buffer, for the same reason as
	// VMFunctionalSpecTimeout.
	VMEnterpriseSpecTimeout = 30 * time.Minute

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
	MDXRemoteWriteUsername = "monitoring-4"

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
	// KafkaRetries is the number of attempts to wait for Kafka-ingested metrics to appear (15 min max).
	KafkaRetries = int((15 * time.Minute).Seconds() / DataPropagationDelay.Seconds())
)

// cell is a lock-free holder for a single configuration value. It replaces the
// repeated per-field "mu.Lock(); defer mu.Unlock(); return/set field" boilerplate that
// used to back every Set*/Get* pair in this file.
type cell[T any] struct {
	v atomic.Pointer[T]
}

func (c *cell[T]) Set(val T) {
	c.v.Store(&val)
}

func (c *cell[T]) Get() T {
	if p := c.v.Load(); p != nil {
		return *p
	}
	var zero T
	return zero
}

var (
	manifestsDirCell cell[string]

	reportLocationCell cell[string]
	envK8SDistroCell   cell[string]

	nginxHostCell cell[string]

	helmChartVersionCell cell[string]
	operatorVersionCell  cell[string]
	vmVersionCell        cell[string]

	vmK8sStackChartVersionCell    cell[string]
	vmDistributedChartVersionCell cell[string]
	vlSingleChartVersionCell      cell[string]
	vlCollectorChartVersionCell   cell[string]
	vlVersionCell                 cell[string]

	operatorImageRegistryCell   cell[string]
	operatorImageRepositoryCell cell[string]
	operatorImageTagCell        cell[string]

	vmSingleDefaultImageCell   cell[string]
	vmSingleDefaultVersionCell cell[string]

	vmClusterVMSelectDefaultImageCell   cell[string]
	vmClusterVMSelectDefaultVersionCell cell[string]

	vmClusterVMStorageDefaultImageCell   cell[string]
	vmClusterVMStorageDefaultVersionCell cell[string]

	vmClusterVMInsertDefaultImageCell   cell[string]
	vmClusterVMInsertDefaultVersionCell cell[string]

	vmAgentDefaultImageCell   cell[string]
	vmAgentDefaultVersionCell cell[string]

	vmAlertDefaultImageCell   cell[string]
	vmAlertDefaultVersionCell cell[string]

	vmAuthDefaultImageCell   cell[string]
	vmAuthDefaultVersionCell cell[string]

	vmBackupDefaultImageCell   cell[string]
	vmBackupDefaultVersionCell cell[string]

	vmRestoreDefaultImageCell   cell[string]
	vmRestoreDefaultVersionCell cell[string]
	licenseFileCell             cell[string]
	distributedRegionCell       cell[string]
	distributedZonesCell        cell[string]
)

// Setters

// SetManifestsDir overrides the base path for manifest files.
func SetManifestsDir(val string) { manifestsDirCell.Set(val) }

// ManifestsRoot returns the base path for manifest files.
func ManifestsRoot() string {
	if v := manifestsDirCell.Get(); v != "" {
		return v
	}
	return "../../manifests"
}

// OverwatchVMAgentYaml returns the path to the overwatch VMAgent manifest.
func OverwatchVMAgentYaml() string { return ManifestsRoot() + "/overwatch/vmagent.yaml" }

// OverwatchVMSingleIngress returns the path to the overwatch VMSingle ingress manifest.
func OverwatchVMSingleIngress() string { return ManifestsRoot() + "/overwatch/vmsingle-ingress.yaml" }

// SmokeValuesFile returns the values file path for smoke tests.
func SmokeValuesFile() string { return ManifestsRoot() + "/helm-values/smoke.yaml" }

// DistributedValuesFile returns the values file path for distributed chart tests.
func DistributedValuesFile() string { return ManifestsRoot() + "/helm-values/distributed.yaml" }

// ChaosMeshValuesFile returns the values file path for chaos mesh.
func ChaosMeshValuesFile() string { return ManifestsRoot() + "/chaos-mesh-operator/values.yaml" }

// KEDAValuesFile returns the values file path for KEDA.
func KEDAValuesFile() string { return ManifestsRoot() + "/keda/values.yaml" }

// VictoriaLogsSingleValuesFile returns the values file path for VictoriaLogs single.
func VictoriaLogsSingleValuesFile() string {
	return ManifestsRoot() + "/helm-values/victoria-logs.yaml"
}

// VictoriaLogsCollectorValuesFile returns the values file path for VictoriaLogs Collector.
func VictoriaLogsCollectorValuesFile() string {
	return ManifestsRoot() + "/helm-values/victoria-logs-collector.yaml"
}

// VPACRDsYaml returns the path to the VPA CRD manifest file.
func VPACRDsYaml() string { return ManifestsRoot() + "/vpa/crds.yaml" }

// LogEmitterYaml returns the path to the log-emitter pod manifest used by the
// VLCollector functional test.
func LogEmitterYaml() string { return ManifestsRoot() + "/components/log-emitter.yaml" }

// GatewayAPIStandardInstallURL returns the Gateway API standard CRD manifest URL.
func GatewayAPIStandardInstallURL() string {
	version := gatewayAPIStandardVersion
	if v := os.Getenv("GATEWAY_API_VERSION"); v != "" {
		version = v
	}
	return fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/standard-install.yaml", version)
}

// SetReportLocation sets the path for test reports.
func SetReportLocation(val string) { reportLocationCell.Set(val) }

// ReportLocation returns the configured report location.
func ReportLocation() string { return reportLocationCell.Get() }

// SetEnvK8SDistro sets the Kubernetes distribution name (e.g., kind, gke).
func SetEnvK8SDistro(val string) { envK8SDistroCell.Set(val) }

// EnvK8SDistro returns the configured Kubernetes distribution.
func EnvK8SDistro() string { return envK8SDistroCell.Get() }

// SetNginxHost sets the external hostname for Nginx ingress.
func SetNginxHost(val string) { nginxHostCell.Set(val) }

// NginxHost returns the configured Nginx host.
func NginxHost() string { return nginxHostCell.Get() }

// SetHelmChartVersion sets the detected Helm chart version.
func SetHelmChartVersion(val string) { helmChartVersionCell.Set(val) }

// SetVMK8sStackChartVersion sets the desired install version for the victoria-metrics-k8s-stack chart.
func SetVMK8sStackChartVersion(val string) { vmK8sStackChartVersionCell.Set(val) }

// VMK8sStackChartVersion returns the desired install version for the victoria-metrics-k8s-stack chart.
func VMK8sStackChartVersion() string { return vmK8sStackChartVersionCell.Get() }

// SetVMDistributedChartVersion sets the desired install version for the victoria-metrics-distributed chart.
func SetVMDistributedChartVersion(val string) { vmDistributedChartVersionCell.Set(val) }

// VMDistributedChartVersion returns the desired install version for the victoria-metrics-distributed chart.
func VMDistributedChartVersion() string { return vmDistributedChartVersionCell.Get() }

// SetVLSingleChartVersion sets the desired install version for the victoria-logs-single chart.
func SetVLSingleChartVersion(val string) { vlSingleChartVersionCell.Set(val) }

// VLSingleChartVersion returns the desired install version for the victoria-logs-single chart.
func VLSingleChartVersion() string { return vlSingleChartVersionCell.Get() }

// SetVLCollectorChartVersion sets the desired install version for the victoria-logs-collector chart.
func SetVLCollectorChartVersion(val string) { vlCollectorChartVersionCell.Set(val) }

// VLCollectorChartVersion returns the desired install version for the victoria-logs-collector chart.
func VLCollectorChartVersion() string { return vlCollectorChartVersionCell.Get() }

// SetVLVersion sets the desired VictoriaLogs image tag.
func SetVLVersion(val string) { vlVersionCell.Set(val) }

// VLVersion returns the desired VictoriaLogs image tag.
func VLVersion() string { return vlVersionCell.Get() }

// SetOperatorVersion sets the detected VictoriaMetrics Operator version.
func SetOperatorVersion(val string) { operatorVersionCell.Set(val) }

// SetVMVersion sets the detected VictoriaMetrics Operator version.
func SetVMVersion(val string) { vmVersionCell.Set(val) }

// SetOperatorImageRegistry sets the operator image registry.
func SetOperatorImageRegistry(val string) { operatorImageRegistryCell.Set(val) }

// SetOperatorImageRepository sets the operator image repository.
func SetOperatorImageRepository(val string) { operatorImageRepositoryCell.Set(val) }

// SetOperatorImageTag sets the operator image tag.
func SetOperatorImageTag(val string) { operatorImageTagCell.Set(val) }

// SetVMSingleDefaultImage sets the default image for VMSingle.
func SetVMSingleDefaultImage(val string) { vmSingleDefaultImageCell.Set(val) }

// SetVMSingleDefaultVersion sets the default version for VMSingle.
func SetVMSingleDefaultVersion(val string) { vmSingleDefaultVersionCell.Set(val) }

// SetVMClusterVMSelectDefaultImage sets the default image for VMCluster VMSelect.
func SetVMClusterVMSelectDefaultImage(val string) { vmClusterVMSelectDefaultImageCell.Set(val) }

// SetVMClusterVMSelectDefaultVersion sets the default version for VMCluster VMSelect.
func SetVMClusterVMSelectDefaultVersion(val string) { vmClusterVMSelectDefaultVersionCell.Set(val) }

// SetVMClusterVMStorageDefaultImage sets the default image for VMCluster VMStorage.
func SetVMClusterVMStorageDefaultImage(val string) { vmClusterVMStorageDefaultImageCell.Set(val) }

// SetVMClusterVMStorageDefaultVersion sets the default version for VMCluster VMStorage.
func SetVMClusterVMStorageDefaultVersion(val string) {
	vmClusterVMStorageDefaultVersionCell.Set(val)
}

// SetVMClusterVMInsertDefaultImage sets the default image for VMCluster VMInsert.
func SetVMClusterVMInsertDefaultImage(val string) { vmClusterVMInsertDefaultImageCell.Set(val) }

// SetVMClusterVMInsertDefaultVersion sets the default version for VMCluster VMInsert.
func SetVMClusterVMInsertDefaultVersion(val string) { vmClusterVMInsertDefaultVersionCell.Set(val) }

// SetVMAgentDefaultImage sets the default image for VMAgent.
func SetVMAgentDefaultImage(val string) { vmAgentDefaultImageCell.Set(val) }

// SetVMAgentDefaultVersion sets the default version for VMAgent.
func SetVMAgentDefaultVersion(val string) { vmAgentDefaultVersionCell.Set(val) }

// SetVMAlertDefaultImage sets the default image for VMAlert.
func SetVMAlertDefaultImage(val string) { vmAlertDefaultImageCell.Set(val) }

// SetVMAlertDefaultVersion sets the default version for VMAlert.
func SetVMAlertDefaultVersion(val string) { vmAlertDefaultVersionCell.Set(val) }

// SetVMAuthDefaultImage sets the default image for VMAuth.
func SetVMAuthDefaultImage(val string) { vmAuthDefaultImageCell.Set(val) }

// SetVMAuthDefaultVersion sets the default version for VMAuth.
func SetVMAuthDefaultVersion(val string) { vmAuthDefaultVersionCell.Set(val) }

// SetVMBackupDefaultImage sets the default image for VMBackup.
func SetVMBackupDefaultImage(val string) { vmBackupDefaultImageCell.Set(val) }

// SetVMBackupDefaultVersion sets the default version for VMBackup.
func SetVMBackupDefaultVersion(val string) { vmBackupDefaultVersionCell.Set(val) }

// SetVMRestoreDefaultImage sets the default image for VMRestore.
func SetVMRestoreDefaultImage(val string) { vmRestoreDefaultImageCell.Set(val) }

// SetVMRestoreDefaultVersion sets the default version for VMRestore.
func SetVMRestoreDefaultVersion(val string) { vmRestoreDefaultVersionCell.Set(val) }

// SetLicenseFile sets the license file path.
func SetLicenseFile(val string) { licenseFileCell.Set(val) }

// SetDistributedRegion sets the region label used by distributed load tests.
func SetDistributedRegion(region string) { distributedRegionCell.Set(region) }

// SetDistributedZones sets the zones label used by distributed load tests.
func SetDistributedZones(zones string) { distributedZonesCell.Set(zones) }

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
	host := NginxHost()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmsingle.%s.nip.io", host)
}

// VMSingleNamespacedHost returns the hostname for VMSingle in the given namespace.
func VMSingleNamespacedHost(namespace string) string {
	host := NginxHost()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmsingle-%s.%s.nip.io", namespace, host)
}

// VMAgentNamespacedHost returns the hostname for VMAgent in the given namespace.
func VMAgentNamespacedHost(namespace string) string {
	host := NginxHost()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmagent-%s.%s.nip.io", namespace, host)
}

// VMAgentNamedHost returns the hostname for a named VMAgent in the given namespace.
// Use this for VMAgents whose CR name differs from "vmagent".
func VMAgentNamedHost(name, namespace string) string {
	host := NginxHost()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s-%s.%s.nip.io", name, namespace, host)
}

// VMSelectHost returns the hostname for VMSelect in the given namespace.
func VMSelectHost(namespace string) string {
	host := NginxHost()
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
	host := NginxHost()
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
	host := NginxHost()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("vmauth-%s.%s.nip.io", namespace, host)
}

// AlertManagerHost returns the hostname for AlertManager in the given namespace.
func AlertManagerHost(namespace string) string {
	host := NginxHost()
	if host == "" {
		return ""
	}
	if namespace == "" {
		return fmt.Sprintf("alert.%s.nip.io", host)
	}
	return fmt.Sprintf("alert-%s.%s.nip.io", namespace, host)
}

// VLHost returns the ingress hostname for default VictoriaLogs single.
func VLHost() string {
	return VLNamespacedHost("")
}

// VLNamespacedHost returns the ingress hostname for VictoriaLogs single in the given namespace.
func VLNamespacedHost(namespace string) string {
	host := NginxHost()
	if host == "" {
		return ""
	}
	if namespace != "" {
		return fmt.Sprintf("vl-%s.%s.nip.io", namespace, host)
	}
	return fmt.Sprintf("vl.%s.nip.io", host)
}

// VLUrl returns the ingress URL for VictoriaLogs single in the given namespace.
func VLUrl(namespace string) string {
	return fmt.Sprintf("http://%s", VLNamespacedHost(namespace))
}

// VLSelectHost returns the ingress hostname for VictoriaLogs cluster select in the given namespace.
func VLSelectHost(namespace string) string {
	host := NginxHost()
	if host == "" {
		return ""
	}
	if namespace == "" {
		return fmt.Sprintf("vlselect.%s.nip.io", host)
	}
	return fmt.Sprintf("vlselect-%s.%s.nip.io", namespace, host)
}

// VLInsertHost returns the ingress hostname for VictoriaLogs cluster insert in the given namespace.
func VLInsertHost(namespace string) string {
	host := NginxHost()
	if host == "" {
		return ""
	}
	if namespace == "" {
		return fmt.Sprintf("vlinsert.%s.nip.io", host)
	}
	return fmt.Sprintf("vlinsert-%s.%s.nip.io", namespace, host)
}

// VLSelectUrl returns the ingress URL for VictoriaLogs cluster select in the given namespace.
func VLSelectUrl(namespace string) string {
	return fmt.Sprintf("http://%s", VLSelectHost(namespace))
}

// VLInsertUrl returns the ingress URL for VictoriaLogs cluster insert in the given namespace.
func VLInsertUrl(namespace string) string {
	return fmt.Sprintf("http://%s", VLInsertHost(namespace))
}

// VMGatherHost returns the hostname for VMGather.
func VMGatherHost() string {
	host := NginxHost()
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

// GetVLInsertSvc returns the internal Kubernetes service address for the vlinsert component
// of a VLCluster deployed by the operator. The service name follows the operator convention:
// vlinsert-<clusterName>.<namespace>.svc.cluster.local:9481
func GetVLInsertSvc(clusterName, namespace string) string {
	return fmt.Sprintf("vlinsert-%s.%s.svc.cluster.local:9481", clusterName, namespace)
}

// GetVLSelectSvc returns the internal Kubernetes service address for the vlselect component
// of a VLCluster deployed by the operator. The service name follows the operator convention:
// vlselect-<clusterName>.<namespace>.svc.cluster.local:9471
func GetVLSelectSvc(clusterName, namespace string) string {
	return fmt.Sprintf("vlselect-%s.%s.svc.cluster.local:9471", clusterName, namespace)
}

// KafkaBrokerSvc returns the in-cluster bootstrap address for the Strimzi Kafka cluster
// deployed in the given namespace by install.InstallKafka.
func KafkaBrokerSvc(namespace string) string {
	return fmt.Sprintf("kafka-kafka-bootstrap.%s.svc.cluster.local:9092", namespace)
}

// HelmChartVersion returns the stored Helm chart version.
func HelmChartVersion() string { return helmChartVersionCell.Get() }

// OperatorVersion returns the stored Operator version.
func OperatorVersion() string { return operatorVersionCell.Get() }

// VMVersion returns the stored Operator version.
func VMVersion() string { return vmVersionCell.Get() }

// OperatorImageRegistry returns the stored operator image registry.
func OperatorImageRegistry() string { return operatorImageRegistryCell.Get() }

// OperatorImageRepository returns the stored operator image repository.
func OperatorImageRepository() string { return operatorImageRepositoryCell.Get() }

// OperatorImageTag returns the stored operator image tag.
func OperatorImageTag() string { return operatorImageTagCell.Get() }

// VMSingleDefaultImage returns the stored VMSingle default image.
func VMSingleDefaultImage() string { return vmSingleDefaultImageCell.Get() }

// VMSingleDefaultVersion returns the stored VMSingle default version.
func VMSingleDefaultVersion() string { return vmSingleDefaultVersionCell.Get() }

// VMClusterVMSelectDefaultImage returns the stored VMCluster VMSelect default image.
func VMClusterVMSelectDefaultImage() string { return vmClusterVMSelectDefaultImageCell.Get() }

// VMClusterVMSelectDefaultVersion returns the stored VMCluster VMSelect default version.
func VMClusterVMSelectDefaultVersion() string { return vmClusterVMSelectDefaultVersionCell.Get() }

// VMClusterVMStorageDefaultImage returns the stored VMCluster VMStorage default image.
func VMClusterVMStorageDefaultImage() string { return vmClusterVMStorageDefaultImageCell.Get() }

// VMClusterVMStorageDefaultVersion returns the stored VMCluster VMStorage default version.
func VMClusterVMStorageDefaultVersion() string { return vmClusterVMStorageDefaultVersionCell.Get() }

// VMClusterVMInsertDefaultImage returns the stored VMCluster VMInsert default image.
func VMClusterVMInsertDefaultImage() string { return vmClusterVMInsertDefaultImageCell.Get() }

// VMClusterVMInsertDefaultVersion returns the stored VMCluster VMInsert default version.
func VMClusterVMInsertDefaultVersion() string { return vmClusterVMInsertDefaultVersionCell.Get() }

// VMAgentDefaultImage returns the stored VMAgent default image.
func VMAgentDefaultImage() string { return vmAgentDefaultImageCell.Get() }

// VMAgentDefaultVersion returns the stored VMAgent default version.
func VMAgentDefaultVersion() string { return vmAgentDefaultVersionCell.Get() }

// VMAlertDefaultImage returns the stored VMAlert default image.
func VMAlertDefaultImage() string { return vmAlertDefaultImageCell.Get() }

// VMAlertDefaultVersion returns the stored VMAlert default version.
func VMAlertDefaultVersion() string { return vmAlertDefaultVersionCell.Get() }

// VMAuthDefaultImage returns the stored VMAuth default image.
func VMAuthDefaultImage() string { return vmAuthDefaultImageCell.Get() }

// VMAuthDefaultVersion returns the stored VMAuth default version.
func VMAuthDefaultVersion() string { return vmAuthDefaultVersionCell.Get() }

// VMBackupDefaultImage returns the stored VMBackup default image.
func VMBackupDefaultImage() string { return vmBackupDefaultImageCell.Get() }

// VMBackupDefaultVersion returns the stored VMBackup default version.
func VMBackupDefaultVersion() string { return vmBackupDefaultVersionCell.Get() }

// VMRestoreDefaultImage returns the stored VMRestore default image.
func VMRestoreDefaultImage() string { return vmRestoreDefaultImageCell.Get() }

// VMRestoreDefaultVersion returns the stored VMRestore default version.
func VMRestoreDefaultVersion() string { return vmRestoreDefaultVersionCell.Get() }

// LicenseFile returns the stored license file path.
func LicenseFile() string { return licenseFileCell.Get() }

// DistributedRegion returns the region label used by distributed load tests.
func DistributedRegion() string { return distributedRegionCell.Get() }

// DistributedZones returns the zones label used by distributed load tests.
func DistributedZones() string { return distributedZonesCell.Get() }

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
