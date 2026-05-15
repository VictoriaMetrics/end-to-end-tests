package install

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2" //nolint
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

// buildVMK8StackValues creates Helm set values for VM component image tags based on the configured VM version.
// It handles the logic for setting appropriate image tags for all VictoriaMetrics components,
// including the special case of adding "-cluster" suffix for cluster components when not using "latest" tag.
func buildVMK8StackValues(namespace string) map[string]string {
	setValues := map[string]string{
		"vmcluster.ingress.select.hosts[0]":                               consts.VMSelectHost(namespace),
		"vmcluster.ingress.insert.hosts[0]":                               consts.VMInsertHost(namespace),
		"alertmanager.ingress.enabled":                                    "true",
		"alertmanager.ingress.hosts[0]":                                   consts.AlertManagerHost(namespace),
		"victoria-metrics-operator.operator.disable_prometheus_converter": "true",
	}

	if consts.OperatorImageRegistry() != "" {
		setValues["victoria-metrics-operator.image.registry"] = consts.OperatorImageRegistry()
	}
	if consts.OperatorImageRepository() != "" {
		setValues["victoria-metrics-operator.image.repository"] = consts.OperatorImageRepository()
	}
	if consts.OperatorImageTag() != "" {
		setValues["victoria-metrics-operator.image.tag"] = consts.OperatorImageTag()
	}

	envIdx := 0
	addEnv := func(name, value string) {
		if value != "" {
			setValues[fmt.Sprintf("victoria-metrics-operator.env[%d].name", envIdx)] = name
			setValues[fmt.Sprintf("victoria-metrics-operator.env[%d].value", envIdx)] = value
			envIdx++
		}
	}

	addEnv("VM_VMSINGLEDEFAULT_IMAGE", consts.VMSingleDefaultImage())
	addEnv("VM_VMSINGLEDEFAULT_VERSION", consts.VMSingleDefaultVersion())
	addEnv("VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_IMAGE", consts.VMClusterVMSelectDefaultImage())
	addEnv("VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_VERSION", consts.VMClusterVMSelectDefaultVersion())
	addEnv("VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_IMAGE", consts.VMClusterVMStorageDefaultImage())
	addEnv("VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_VERSION", consts.VMClusterVMStorageDefaultVersion())
	addEnv("VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_IMAGE", consts.VMClusterVMInsertDefaultImage())
	addEnv("VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_VERSION", consts.VMClusterVMInsertDefaultVersion())
	addEnv("VM_VMAGENTDEFAULT_IMAGE", consts.VMAgentDefaultImage())
	addEnv("VM_VMAGENTDEFAULT_VERSION", consts.VMAgentDefaultVersion())
	addEnv("VM_VMALERTDEFAULT_IMAGE", consts.VMAlertDefaultImage())
	addEnv("VM_VMALERTDEFAULT_VERSION", consts.VMAlertDefaultVersion())
	addEnv("VM_VMAUTHDEFAULT_IMAGE", consts.VMAuthDefaultImage())
	addEnv("VM_VMAUTHDEFAULT_VERSION", consts.VMAuthDefaultVersion())

	return setValues
}

// InstallVMK8StackWithHelm installs or upgrades a Helm chart into the specified namespace and waits for key operator
// and component deployments to become available. The function also reads version labels from deployed resources
// and stores them in package-level consts for later use by tests.
//
// Parameters:
// - ctx: parent context for the operation (not used directly for Helm invocation here).
// - helmChart: path or name of the Helm chart to install/upgrade.
// - valuesFile: path to the Helm values file to apply.
// - t: terratest testing interface for running commands and assertions.
// - namespace: Kubernetes namespace for the release.
// - releaseName: Helm release name to use for the upgrade.
func InstallVMK8StackWithHelm(ctx context.Context, helmChart, valuesFile string, t terratesting.TestingT, namespace string, releaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	setValues := buildVMK8StackValues(namespace)

	setFiles := map[string]string{}
	if consts.LicenseFile() != "" {
		setFiles["global.license.key"] = consts.LicenseFile()
	}

	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VMK8sStackChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}
	helmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{valuesFile},
		SetValues:      setValues,
		SetFiles:       setFiles,
		ExtraArgs: map[string][]string{
			"upgrade": upgradeArgs,
		},
	}

	By(fmt.Sprintf("Install %s chart", helmChart))
	err := helm.UpgradeE(t, helmOpts, helmChart, releaseName)
	if err != nil {
		t.Fatalf("Failed to install chart %s: %v", helmChart, err)
	}

	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "vmks-victoria-metrics-operator", consts.Retries, consts.PollingInterval)
	vmOperator := k8s.GetDeploymentContext(t, ctx, kubeOpts, "vmks-victoria-metrics-operator")
	operatorVersion := vmOperator.Labels["app.kubernetes.io/version"]
	if operatorVersion == "" {
		helpers.Logf("WARNING: app.kubernetes.io/version label is empty/missing on vmks-victoria-metrics-operator deployment.")
		helpers.Logf("Available labels on vmks-victoria-metrics-operator: %+v", vmOperator.Labels)

		helpers.Logf("Found operator version label: %s", operatorVersion)
	}
	consts.SetOperatorVersion(operatorVersion)

	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "vmagent-vmks", consts.Retries, consts.PollingInterval)
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "vmalert-vmks", consts.Retries, consts.PollingInterval)
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "vminsert-vmks", consts.Retries, consts.PollingInterval)
	require.Eventually(t, func() bool {
		_, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pod", "-l", "app.kubernetes.io/name=vmalertmanager", "--timeout=300s")
		return err == nil
	}, consts.ResourceWaitTimeout, consts.PollingInterval)

	// Extract version information from ingress labels
	vmSelectIngress := k8s.GetIngressContext(t, ctx, kubeOpts, "vmselect-vmks")
	vmVersion := vmSelectIngress.Labels["app.kubernetes.io/version"]
	if vmVersion == "" {
		helpers.Logf("WARNING: app.kubernetes.io/version label is empty/missing on vmselect-vmks ingress.")
		helpers.Logf("Available labels on vmselect-vmks ingress: %+v", vmSelectIngress.Labels)

		helpers.Logf("Found VM version label: %s", vmVersion)
	}
	consts.SetVMVersion(vmVersion)

	helmChartVersion := vmOperator.Labels["helm.sh/chart"]
	if helmChartVersion == "" {
		helpers.Logf("WARNING: helm.sh/chart label is empty/missing on vmks-victoria-metrics-operator deployment.")
		helpers.Logf("Available labels on vmks-victoria-metrics-operator: %+v", vmOperator.Labels)

		helpers.Logf("Found helm.sh/chart label: %s", helmChartVersion)
	}
	consts.SetHelmChartVersion(helmChartVersion)

	// Setup VMNodeScrape to get cadvisor metrics
	manifestPath := consts.ManifestsRoot() + "/node-scrape.yaml"
	KubectlApply(ctx, t, kubeOpts, manifestPath)
}

// buildVMDistributedValues creates Helm set values for VM component image tags based on the configured VM version.
// It handles the logic for setting appropriate image tags for all VictoriaMetrics components,
// including the special case of adding "-cluster" suffix for cluster components when not using "latest" tag.
func buildVMDistributedValues(namespace string) map[string]string {
	setValues := map[string]string{
		"read.global.vmauth.spec.ingress.host":  consts.VMSelectHost(namespace),
		"write.global.vmauth.spec.ingress.host": consts.VMInsertHost(namespace),
	}

	// Set region-specific ingress hosts
	setValues["zoneTpl.read.vmauth.spec.ingress.host"] = fmt.Sprintf("vmselect-{{ (.zone).name }}.%s.nip.io", consts.NginxHost())

	zones := strings.Split(consts.DistributedZones(), ",")
	for i, zone := range zones {
		if trimmedZone := strings.TrimSpace(zone); trimmedZone != "" {
			setValues[fmt.Sprintf("availabilityZones[%d].name", i)] = trimmedZone
		}
	}

	return setValues
}

func buildVMDistributedSetFiles() map[string]string {
	setFiles := map[string]string{}
	if consts.LicenseFile() == "" {
		return setFiles
	}

	setFiles["global.license.key"] = consts.LicenseFile()
	setFiles["common.vmcluster.spec.license.key"] = consts.LicenseFile()
	setFiles["common.vmsingle.spec.license.key"] = consts.LicenseFile()
	return setFiles
}

// InstallVMDistributedWithHelm installs or upgrades a Helm chart into the specified namespace and waits for key
// component deployments to become available.
//
// Parameters:
// - ctx: parent context for the operation (not used directly for Helm invocation here).
// - helmChart: path or name of the Helm chart to install/upgrade.
// - valuesFile: path to the Helm values file to apply.
// - t: terratest testing interface for running commands and assertions.
// - namespace: Kubernetes namespace for the release.
// - releaseName: Helm release name to use for the upgrade.
func InstallVMDistributedWithHelm(ctx context.Context, helmChart, valuesFile string, t terratesting.TestingT, namespace string, releaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	setValues := buildVMDistributedValues(namespace)
	setFiles := buildVMDistributedSetFiles()

	upgradeArgsDistributed := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VMDistributedChartVersion(); v != "" {
		upgradeArgsDistributed = append(upgradeArgsDistributed, "--version", v)
	}
	helmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{valuesFile},
		SetValues:      setValues,
		SetFiles:       setFiles,
		ExtraArgs: map[string][]string{
			"upgrade": upgradeArgsDistributed,
		},
	}

	By(fmt.Sprintf("Install %s chart", helmChart))
	err := helm.UpgradeE(t, helmOpts, helmChart, releaseName)
	if err != nil {
		t.Fatalf("Failed to install chart %s: %v", helmChart, err)
	}

	for _, vmAuthType := range []string{"read", "write"} {
		vmAuthName := fmt.Sprintf("vmauth-vmauth-global-%s-vmks-vm-distributed", vmAuthType)
		k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, vmAuthName, consts.Retries, consts.PollingInterval)
		// k8s.WaitUntilIngressAvailable(t, kubeOpts, vmAuthName, consts.Retries, consts.PollingInterval)
	}

	vmclient := GetVMClient(t, kubeOpts)
	WaitForVMAgentToBeOperational(ctx, t, kubeOpts, namespace, vmclient)
	WaitForVMClusterToBeOperational(ctx, t, kubeOpts, namespace, vmclient)
}

// InstallOverwatch provisions a lightweight VMSingle overwatch instance and a VMAgent that forwards data to it.
//
// The function creates resources from manifests under the manifests/overwatch directory, adjusts ingress hosts
// and VMAgent configuration to point to the dynamically determined service addresses, and waits for both VMAgent
// and VMSingle to become operational.
//
// Parameters:
// - ctx: context used for waiting operations (timeouts are applied by the underlying wait functions).
// - t: terratest testing interface for running commands and assertions.
// - namespace: Kubernetes namespace in which to install the overwatch ingress and related resources.
// - vmAgentNamespace: Namespace where the VMAgent instance lives (may differ from the overwatch namespace).
// - vmAgentReleaseName: Release name of the VMAgent (used when waiting for VMAgent readiness).
func InstallOverwatch(ctx context.Context, t terratesting.TestingT, namespace, vmAgentNamespace, vmAgentReleaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}
	vmclient := GetVMClient(t, kubeOpts)

	By("Install VMSingle overwatch instance")

	patchAndApplyVMSingleManifest(ctx, t, kubeOpts, namespace, consts.OverwatchVMSingleYaml(), nil)
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "vmsingle-overwatch", consts.Retries, consts.PollingInterval)

	By("Install VMSingle ingress")
	ExposeVMSingleAsIngress(ctx, t, kubeOpts, namespace)

	By("Reconfigure VMAgent to send data to VMSingle")
	// Read vmagent.yaml content
	vmagentYamlPath := consts.OverwatchVMAgentYaml()
	vmagentYaml, err := os.ReadFile(vmagentYamlPath)
	require.NoError(t, err)

	// Replace URLs with dynamic service addresses
	vmSingleSvc := consts.GetVMSingleSvc("overwatch", namespace)
	oldVMSingleURL := "http://vmsingle-overwatch.vm.svc.cluster.local.:8428/prometheus/api/v1/write"
	newVMSingleURL := fmt.Sprintf("http://%s/prometheus/api/v1/write", vmSingleSvc)
	updatedVmagentYaml := strings.ReplaceAll(string(vmagentYaml), oldVMSingleURL, newVMSingleURL)

	// Apply the updated vmagent configuration
	kubeOpts = k8s.NewKubectlOptions("", "", vmAgentNamespace)
	KubectlApplyFromString(ctx, t, kubeOpts, updatedVmagentYaml)

	By("Wait for VMAgent to become operational")
	WaitForVMAgentToBeOperational(ctx, t, kubeOpts, vmAgentNamespace, vmclient)

	By("Reconfigure VMAlert to read data from VMSingle")
	ReconfigureVMAlert(ctx, t, vmAgentNamespace, vmAgentReleaseName, consts.GetVMSingleSvc("overwatch", namespace))
	WaitForVMAlertToBeOperational(ctx, t, kubeOpts, vmAgentNamespace, vmclient)

	By("Wait for overwatch VMSingle to become operational")
	WaitForVMSingleToBeOperational(ctx, t, kubeOpts, namespace, vmclient)

}
