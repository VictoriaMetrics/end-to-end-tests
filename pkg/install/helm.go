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
	"sigs.k8s.io/yaml"

	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

// buildVMK8StackValues creates Helm set values for VM component image tags based on the configured VM version.
// It handles the logic for setting appropriate image tags for all VictoriaMetrics components,
// including the special case of adding "-cluster" suffix for cluster components when not using "latest" tag.
func buildVMK8StackValues(namespace string) map[string]string {
	setValues := map[string]string{
		"vmsingle.ingress.hosts[0]":                                       consts.VMSingleHost(),
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
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "vmsingle-vmks", consts.Retries, consts.PollingInterval)
	require.Eventually(t, func() bool {
		_, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pod", "-l", "app.kubernetes.io/name=vmalertmanager", "--timeout=300s")
		return err == nil
	}, consts.ResourceWaitTimeout, consts.PollingInterval)

	// Extract VM version from VMSingle CR spec (operator-managed, no app.kubernetes.io/version label on deployment)
	vmVersion := vmVersionFromCR(t, ctx, kubeOpts, releaseName)
	helpers.Logf("Found VM version: %s", vmVersion)
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

// vmVersionFromCR reads the VM version from the app.kubernetes.io/version annotation on the VMSingle CR.
func vmVersionFromCR(t terratesting.TestingT, ctx context.Context, kubeOpts *k8s.KubectlOptions, releaseName string) string {
	out, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "get", "vmsingle", releaseName, "-o", `jsonpath={.metadata.labels.app\.kubernetes\.io/version}`)
	if err != nil {
		helpers.Logf("WARNING: failed to get VMSingle %s: %v", releaseName, err)
		return ""
	}
	if out = strings.TrimSpace(out); out == "" {
		helpers.Logf("WARNING: VMSingle %s has no app.kubernetes.io/version annotation", releaseName)
	}
	return out
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

// InstallVictoriaLogs installs VictoriaLogs single-node and VictoriaLogs Collector via Helm.
// The collector is configured to ship pod logs to the VictoriaLogs single instance.
//
// Parameters:
// - ctx: parent context for the operation.
// - t: terratest testing interface for running commands and assertions.
// - namespace: Kubernetes namespace for both releases.
// - releaseName: Helm release name for the VictoriaLogs single instance.
// - collectorReleaseName: Helm release name for the VictoriaLogs Collector.
func InstallVictoriaLogs(ctx context.Context, t terratesting.TestingT, namespace, releaseName, collectorReleaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)

	// Install victoria-logs-single
	singleUpgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLSingleChartVersion(); v != "" {
		singleUpgradeArgs = append(singleUpgradeArgs, "--version", v)
	}
	singleHelmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.VictoriaLogsSingleValuesFile()},
		SetValues: map[string]string{
			"server.ingress.enabled":          "true",
			"server.ingress.ingressClassName": "nginx",
			"server.ingress.hosts[0].name":    consts.VLHost(),
			"server.ingress.hosts[0].path[0]": "/",
		},
		ExtraArgs: map[string][]string{
			"upgrade": singleUpgradeArgs,
		},
	}

	By(fmt.Sprintf("Install %s chart", consts.VictoriaLogsSingleChart))
	err := helm.UpgradeE(t, singleHelmOpts, consts.VictoriaLogsSingleChart, releaseName)
	if err != nil {
		t.Fatalf("Failed to install chart %s: %v", consts.VictoriaLogsSingleChart, err)
	}

	require.Eventually(t, func() bool {
		_, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pod", "-l", fmt.Sprintf("app.kubernetes.io/instance=%s", releaseName), "--timeout=300s")
		return err == nil
	}, consts.ResourceWaitTimeout, consts.PollingInterval)

	// Install victoria-logs-collector pointing to the single instance
	vlSingleURL := fmt.Sprintf("http://%s", consts.GetVLSingleSvc(releaseName, namespace))
	collectorUpgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLCollectorChartVersion(); v != "" {
		collectorUpgradeArgs = append(collectorUpgradeArgs, "--version", v)
	}
	collectorHelmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.VictoriaLogsCollectorValuesFile()},
		SetValues: map[string]string{
			"remoteWrite[0].url": vlSingleURL,
		},
		ExtraArgs: map[string][]string{
			"upgrade": collectorUpgradeArgs,
		},
	}

	By(fmt.Sprintf("Install %s chart", consts.VictoriaLogsCollectorChart))
	err = helm.UpgradeE(t, collectorHelmOpts, consts.VictoriaLogsCollectorChart, collectorReleaseName)
	if err != nil {
		t.Fatalf("Failed to install chart %s: %v", consts.VictoriaLogsCollectorChart, err)
	}
}

// SetVMOperatorEnv sets an env var on the already-installed VictoriaMetrics operator and waits for rollout.
func SetVMOperatorEnv(ctx context.Context, t terratesting.TestingT, namespace, name, value string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	deploymentName := fmt.Sprintf("%s-victoria-metrics-operator", consts.DefaultReleaseName)
	k8s.RunKubectlContext(t, ctx, kubeOpts, "set", "env", fmt.Sprintf("deployment/%s", deploymentName), fmt.Sprintf("%s=%s", name, value))
	k8s.RunKubectlContext(t, ctx, kubeOpts, "rollout", "status", fmt.Sprintf("deployment/%s", deploymentName), "--timeout=120s")
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, deploymentName, consts.Retries, consts.PollingInterval)
}

// InstallOverwatch configures VMAgent to forward data to the monitoring VMSingle instance
// and reconfigures VMAlert to use it as datasource.
//
// The monitoring VMSingle is deployed as part of the k8s-stack Helm release (vmks) in vmAgentNamespace.
// This function does not install a separate overwatch VMSingle.
//
// Parameters:
// - ctx: context used for waiting operations (timeouts are applied by the underlying wait functions).
// - t: terratest testing interface for running commands and assertions.
// - namespace: unused, kept for API compatibility.
// - vmAgentNamespace: Namespace where VMAgent and VMSingle live (k8s-stack namespace).
// - vmAgentReleaseName: Release name of the VMAgent (used when waiting for VMAgent readiness).
func InstallOverwatch(ctx context.Context, t terratesting.TestingT, namespace, vmAgentNamespace, vmAgentReleaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", vmAgentNamespace)
	vmclient := GetVMClient(t, kubeOpts)

	By("Reconfigure VMAgent to send data to monitoring VMSingle")
	// Read vmagent.yaml content
	vmagentYamlPath := consts.OverwatchVMAgentYaml()
	vmagentYaml, err := os.ReadFile(vmagentYamlPath)
	require.NoError(t, err)

	// Replace URLs with monitoring VMSingle service address
	vmSingleSvc := consts.GetVMSingleSvc(vmAgentReleaseName, vmAgentNamespace)
	oldVMSingleURL := "http://vmsingle-overwatch.vm.svc.cluster.local.:8428/prometheus/api/v1/write"
	newVMSingleURL := fmt.Sprintf("http://%s/prometheus/api/v1/write", vmSingleSvc)
	updatedVmagentYaml := strings.ReplaceAll(string(vmagentYaml), oldVMSingleURL, newVMSingleURL)

	var vmAgent vmv1beta1.VMAgent
	unmarshalErr := yaml.Unmarshal([]byte(updatedVmagentYaml), &vmAgent)
	require.NoError(t, unmarshalErr)

	if clusterID := strings.TrimSpace(os.Getenv("CLUSTER_ID")); clusterID != "" {
		vmAgent.Spec.InlineRelabelConfig = append(vmAgent.Spec.InlineRelabelConfig, &vmv1beta1.RelabelConfig{
			TargetLabel: "cluster_id",
			Replacement: &clusterID,
		})
	}

	if mdxPasswordPath := os.Getenv("MDX_PASSWORD"); mdxPasswordPath != "" {
		By("Configuring VMAgent to send data to central monitoring")

		passwordBytes, readErr := os.ReadFile(mdxPasswordPath)
		require.NoError(t, readErr)

		secret := corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      consts.MDXRemoteWriteSecretName,
				Namespace: vmAgentNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			StringData: map[string]string{
				"username": consts.MDXRemoteWriteUsername,
				"password": strings.TrimSpace(string(passwordBytes)),
			},
		}
		secretYaml, marshalErr := yaml.Marshal(secret)
		require.NoError(t, marshalErr)
		KubectlApplyFromString(ctx, t, kubeOpts, string(secretYaml))

		vmAppLabel := "true"
		vmAgent.Spec.RemoteWrite = append(vmAgent.Spec.RemoteWrite, vmv1beta1.VMAgentRemoteWriteSpec{
			URL: consts.MDXRemoteWriteURL,
			BasicAuth: &vmv1beta1.BasicAuth{
				Username: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: consts.MDXRemoteWriteSecretName},
					Key:                  "username",
				},
				Password: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: consts.MDXRemoteWriteSecretName},
					Key:                  "password",
				},
			},
			InlineUrlRelabelConfig: []*vmv1beta1.RelabelConfig{
				{
					TargetLabel: "victoriametrics_app",
					Replacement: &vmAppLabel,
				},
			},
		})

	}

	enrichedYaml, marshalErr := yaml.Marshal(vmAgent)
	require.NoError(t, marshalErr)
	updatedVmagentYaml = string(enrichedYaml)

	// Apply the updated vmagent configuration
	KubectlApplyFromString(ctx, t, kubeOpts, updatedVmagentYaml)

	By("Wait for VMAgent to become operational")
	WaitForVMAgentToBeOperational(ctx, t, kubeOpts, vmAgentNamespace, vmclient)

	By("Reconfigure VMAlert to read data from monitoring VMSingle")
	ReconfigureVMAlert(ctx, t, vmAgentNamespace, vmAgentReleaseName, consts.GetVMSingleSvc(vmAgentReleaseName, vmAgentNamespace))
	WaitForVMAlertToBeOperational(ctx, t, kubeOpts, vmAgentNamespace, vmclient)

	By("Wait for monitoring VMSingle to become operational")
	WaitForVMSingleToBeOperational(ctx, t, kubeOpts, vmAgentNamespace, vmclient)
}
