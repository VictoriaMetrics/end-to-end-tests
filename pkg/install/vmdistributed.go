package install

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

var chaosNameSanitizer = regexp.MustCompile(`[^a-z0-9-]`)

// InstallVMDistributed applies a VMDistributed operator resource into the target namespace
// and waits for the VMAuth deployment and all zone VMAgents/VMClusters to become operational.
func InstallVMDistributed(ctx context.Context, t terratesting.TestingT, namespace, releaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)

	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}

	ensureVMClusterLicenseSecret(t, kubeOpts, namespace)

	vmAuthHost := consts.VMAuthHost(namespace)
	manifest := buildVMDistributedManifest(releaseName, namespace, vmAuthHost)

	helpers.Logf("Installing VMDistributed %s in namespace %s", releaseName, namespace)
	KubectlApplyFromString(ctx, t, kubeOpts, manifest)

	// VMAuth deployment name follows operator convention: vmauth-<cr-name>
	vmAuthDeployment := "vmauth-" + releaseName
	helpers.Logf("Waiting for VMAuth deployment %s in namespace %s", vmAuthDeployment, namespace)
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, vmAuthDeployment, consts.Retries, consts.PollingInterval)

	vmClient := GetVMClient(t, kubeOpts)
	WaitForVMAgentToBeOperational(ctx, t, kubeOpts, namespace, vmClient)
	WaitForVMClusterToBeOperational(ctx, t, kubeOpts, namespace, vmClient)
}

// VMDistributedRemoteWriteURL returns VMAuth tenant-0 remote write URL.
func VMDistributedRemoteWriteURL(namespace string) string {
	return fmt.Sprintf("http://%s%s", consts.VMAuthHost(namespace), fmt.Sprintf(consts.TenantInsertPathFormat, 0))
}

// ApplyVMDistributedZoneDisruptionChaos blocks all network for one VMDistributed zone.
func ApplyVMDistributedZoneDisruptionChaos(ctx context.Context, t terratesting.TestingT, namespace, zone string, duration time.Duration) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	KubectlApplyFromStringWithRetry(ctx, t, kubeOpts, buildVMDistributedZoneDisruptionChaos(namespace, zone, duration))
}

func buildVMDistributedZoneDisruptionChaos(namespace, zone string, duration time.Duration) string {
	name := chaosNameSanitizer.ReplaceAllString(strings.ToLower(zone), "-")
	return fmt.Sprintf(`apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: zone-disruption-%s
spec:
  selector:
    namespaces:
      - %s
    labelSelectors:
      app.kubernetes.io/instance: %s
  mode: all
  action: loss
  duration: %s
  loss:
    loss: '100'
    correlation: '0'
  direction: both
`, name, namespace, zone, chaosDuration(duration))
}

func chaosDuration(duration time.Duration) string {
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	return duration.Round(time.Second).String()
}

// buildVMDistributedManifest generates a VMDistributed CR manifest for the given parameters.
func buildVMDistributedManifest(releaseName, namespace, vmAuthHost string) string {
	zones := strings.Split(consts.DistributedZones(), ",")
	var zonesYAML strings.Builder
	for _, zone := range zones {
		zone = strings.TrimSpace(zone)
		if zone != "" {
			fmt.Fprintf(&zonesYAML, "  - name: %s\n", zone)
		}
	}

	licenseYAML := ""
	if consts.LicenseFile() != "" {
		licenseYAML = fmt.Sprintf(`
      license:
        keyRef:
          name: %s
          key: %s`, consts.LicenseSecretName, consts.LicenseSecretKey)
	}

	return fmt.Sprintf(`apiVersion: operator.victoriametrics.com/v1alpha1
kind: VMDistributed
metadata:
  name: %s
  namespace: %s
spec:
  vmauth:
    spec:%s
      ingress:
        class_name: nginx
        host: %s
  zoneCommon:
    vmcluster:
      spec:%s
        retentionPeriod: "14"
        replicationFactor: 2
        requestsLoadBalancer:
          enabled: true
          spec:
            replicaCount: 2
        vmstorage:
          replicaCount: 2
          storageDataPath: /vm-data
          nodeSelector:
            monitoring: "true"
          tolerations:
            - key: monitoring
              value: "true"
              effect: NoSchedule
        vmselect:
          replicaCount: 2
          port: "8481"
          nodeSelector:
            monitoring: "true"
          tolerations:
            - key: monitoring
              value: "true"
              effect: NoSchedule
        vminsert:
          replicaCount: 2
          port: "8480"
          nodeSelector:
            monitoring: "true"
          tolerations:
            - key: monitoring
              value: "true"
              effect: NoSchedule
    vmagent:
      spec:%s
        nodeSelector:
          monitoring: "true"
        tolerations:
          - key: monitoring
            value: "true"
            effect: NoSchedule
  zones:
%s`,
		releaseName, namespace,
		licenseYAML, vmAuthHost,
		licenseYAML,
		licenseYAML,
		zonesYAML.String())
}
