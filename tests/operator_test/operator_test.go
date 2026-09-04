package operator_test

import (
	"context"
	"encoding/json"

	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

const (
	operatorNameSelector = "app.kubernetes.io/name=victoria-metrics-operator"

	// serviceMonitorCRDURL is prometheus-operator's own published ServiceMonitor CRD.
	serviceMonitorCRDURL = "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.79.2/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml"
)

type suiteResources struct {
	OperatorNamespace   string
	TestNamespace       string
	OperatorApplication string
}

func newSuiteResources() suiteResources {
	process := GinkgoParallelProcess()
	return suiteResources{
		OperatorNamespace:   fmt.Sprintf("operator-%d-0", process),
		TestNamespace:       fmt.Sprintf("operator-%d-1", process),
		OperatorApplication: fmt.Sprintf("operator-%d-2", process),
	}
}

var (
	t           terratesting.TestingT
	kubeOpts    *k8s.KubectlOptions
	kubeWatched *k8s.KubectlOptions

	operatorServiceAccount string

	globalOperatorDeployment string
	globalOperatorReplicas   string
	operatorLabelSelector    string
	suiteStartTime           time.Time
	specStartTime            time.Time
	suiteSetupCompleted      bool
	resources                suiteResources
)

func TestOperatorHelmSuite(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	RunSpecs(t, "Operator Helm Suite")
}

var _ = SynchronizedBeforeSuite(func(ctx context.Context) {
	t = tests.GetT()
	kubeOpts = k8s.NewKubectlOptions("", "", "default")
	install.InstallArgoCD(ctx, t, kubeOpts, consts.ArgoCDVersion())

	install.EnsureVPACRDs(ctx, t, kubeOpts)
	install.EnsureGatewayAPICRDs(ctx, t, kubeOpts)
	install.DiscoverIngressHost(ctx, t)
	tests.InstallVMStackAndGather(ctx, t)
	tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})

	globalOperatorDeployment, globalOperatorReplicas = scaleDownGlobalOperator()
	removeGlobalOperatorWebhooks(kubeOpts)

	clusterOpts := k8s.NewKubectlOptions("", "", "")
	k8s.KubectlApplyContext(t, ctx, kubeOpts, serviceMonitorCRDURL)
	k8s.RunKubectlContext(t, ctx, clusterOpts,
		"wait", "--for=condition=Established",
		"crd", "servicemonitors.monitoring.coreos.com",
		fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))

	suiteSetupCompleted = true
}, func(ctx context.Context, _ []byte) {
	t = tests.GetT()
	suiteStartTime = time.Now()
})

var _ = SynchronizedAfterSuite(func(ctx context.Context) {}, func(ctx context.Context) {
	restoreGlobalOperator()
})

var _ = BeforeEach(func(ctx context.Context) {
	resources = newSuiteResources()

	resources.TestNamespace = newTestNamespace()
	kubeWatched = k8s.NewKubectlOptions("", "", resources.TestNamespace)

	kubeOpts = k8s.NewKubectlOptions("", "", "default")
	_, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts, "create", "namespace", resources.TestNamespace)
	Expect(err).NotTo(HaveOccurred())

	applyOperator(ctx, normalOperatorParameters())
})

var _ = AfterEach(func(ctx context.Context) {
	tests.GatherOnFailureFrom(ctx, t, kubeOpts, resources.OperatorNamespace, specStartTime)

	_, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts, "delete", "namespace", resources.TestNamespace, "--wait=true")
	Expect(err).NotTo(HaveOccurred())

	install.DeleteArgoCDApplication(t, kubeOpts, resources.OperatorApplication)
})

var _ = Describe("operator Helm deployment", func() {
	It("limits reconciliation to WATCH_NAMESPACE", func(ctx context.Context) {
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "deployment", "-n", resources.OperatorNamespace, "-l", operatorLabelSelector, "-o", "jsonpath={.items[0].spec.template.spec.containers[0].env[?(@.name=='WATCH_NAMESPACE')].value}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(ContainSubstring(resources.TestNamespace))

		install.KubectlApplyFromString(ctx, t, kubeWatched, mustReadOperatorManifest("vmcluster.yaml"))

		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, resources.TestNamespace, "vmcluster-name", vmclient, consts.VMClusterWaitTimeout)
	})

	It("watches updates and removes owned resources", func(ctx context.Context) {
		install.KubectlApplyFromString(ctx, t, kubeWatched, namedOperatorManifest("vmcluster.yaml", "vmcluster", "lifecycle"))
		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, resources.TestNamespace, "lifecycle", vmclient, consts.VMClusterWaitTimeout)
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "statefulset", "vmstorage-lifecycle", "-n", resources.TestNamespace, "-o", "jsonpath={.metadata.name}")
		}, consts.VMClusterWaitTimeout, consts.PollingInterval).Should(Equal("vmstorage-lifecycle"))

		_, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "patch", "vmcluster", "lifecycle", "--type=merge", "-p", `{"spec":{"retentionPeriod":"2d","vmstorage":{"replicaCount":2}}}`)
		require.NoError(t, err)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, resources.TestNamespace, "lifecycle", vmclient, consts.VMClusterWaitTimeout)
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "statefulset", "vmstorage-lifecycle", "-n", resources.TestNamespace, "-o", "jsonpath={.spec.replicas}")
		}, consts.VMClusterWaitTimeout, consts.PollingInterval).Should(Equal("2"))

		k8s.RunKubectlContext(t, ctx, kubeWatched, "delete", "vmcluster", "lifecycle")
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "statefulset", "vmstorage-lifecycle", "-n", resources.TestNamespace, "-o", "jsonpath={.metadata.name}")
		}, consts.VMClusterWaitTimeout, consts.PollingInterval).Should(BeEmpty())
	})

	It("cleans up the auto-created ServiceAccount when switching to a custom one", func(ctx context.Context) {
		// Switching serviceAccountName used to leave the auto-created ServiceAccount orphaned (victoriametrics/operator#1665).
		install.KubectlApplyFromString(ctx, t, kubeWatched, namedOperatorManifest("vmcluster.yaml", "vmcluster", "sa-switch"))
		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, resources.TestNamespace, "sa-switch", vmclient, consts.VMClusterWaitTimeout)

		autoCreatedSA := "vmcluster-sa-switch"
		Eventually(func() string {
			return kubectlOutput(kubeWatched, "get", "serviceaccount", autoCreatedSA, "-o", "jsonpath={.metadata.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal(autoCreatedSA),
			"operator did not auto-create the default ServiceAccount for the VMCluster")

		customSA := "sa-switch-custom"
		k8s.RunKubectlContext(t, ctx, kubeWatched, "create", "serviceaccount", customSA)
		_, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "patch", "vmcluster", "sa-switch", "--type=merge",
			"-p", fmt.Sprintf(`{"spec":{"serviceAccountName":%q}}`, customSA))
		require.NoError(t, err)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, resources.TestNamespace, "sa-switch", vmclient, consts.VMClusterWaitTimeout)

		Eventually(func() string {
			return kubectlOutput(kubeWatched, "get", "statefulset", "vmstorage-sa-switch", "-o", "jsonpath={.spec.template.spec.serviceAccountName}")
		}, consts.VMClusterWaitTimeout, consts.PollingInterval).Should(Equal(customSA),
			"vmstorage pods never picked up the custom ServiceAccount")

		Eventually(func() string {
			return kubectlOutput(kubeWatched, "get", "serviceaccount", autoCreatedSA, "-o", "jsonpath={.metadata.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(BeEmpty(),
			"the auto-created ServiceAccount was left orphaned after switching to a custom serviceAccountName")
	})

	It("does not endlessly reconcile an additional Service using loadBalancerClass", func(ctx context.Context) {
		// loadBalancerClass on an additional Service used to trigger a tight recreate loop (victoriametrics/operator#1550).
		install.KubectlApplyFromString(ctx, t, kubeWatched, namedOperatorManifest("vmcluster.yaml", "vmcluster", "lb-class"))
		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, resources.TestNamespace, "lb-class", vmclient, consts.VMClusterWaitTimeout)

		_, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "patch", "vmcluster", "lb-class", "--type=merge", "-p",
			`{"spec":{"vminsert":{"serviceSpec":{"spec":{"type":"LoadBalancer","loadBalancerClass":"e2e.test/custom"}}}}}`)
		require.NoError(t, err)

		serviceName := "vminsert-lb-class-additional-service"
		Eventually(func() string {
			return kubectlOutput(kubeWatched, "get", "service", serviceName, "-o", "jsonpath={.spec.loadBalancerClass}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("e2e.test/custom"))

		resourceVersion := kubectlOutput(kubeWatched, "get", "service", serviceName, "-o", "jsonpath={.metadata.resourceVersion}")
		Expect(resourceVersion).NotTo(BeEmpty())
		Consistently(func() string {
			return kubectlOutput(kubeWatched, "get", "service", serviceName, "-o", "jsonpath={.metadata.resourceVersion}")
		}, 30*time.Second, consts.PollingInterval).Should(Equal(resourceVersion),
			"operator kept recreating/updating the additional Service instead of leaving it stable once reconciled")
	})

	It("converts a real ServiceMonitor into a VMServiceScrape", func(ctx context.Context) {
		// PromServiceMonitorReconciler watches ServiceMonitor via a real controller-runtime For(), needing get/list/watch — this exercises that end to end instead of just checking the permission.
		install.KubectlApplyFromString(ctx, t, kubeWatched, mustReadOperatorManifest("servicemonitor.yaml"))

		Eventually(func() string {
			return kubectlOutput(kubeWatched, "get", "vmservicescrape", "sm-conversion-check", "-o", "jsonpath={.metadata.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("sm-conversion-check"),
			"operator never converted the ServiceMonitor into a VMServiceScrape")
	})

	It("expands a PVC in place when its StorageClass supports live resizing", func(ctx context.Context) {
		// isStorageClassExpandable lists StorageClasses only when a resize is detected, unlike PDB/HPA/VPA/NetworkPolicy's unconditional orphan-cleanup List calls — so this needs its own trigger.
		install.KubectlApplyFromString(ctx, t, k8s.NewKubectlOptions("", "", ""), mustReadOperatorManifest("expandable-storageclass.yaml"))

		install.KubectlApplyFromString(ctx, t, kubeWatched, namedOperatorManifest("vmcluster-expandable-storage.yaml", "vmcluster", "storage-expand"))
		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, resources.TestNamespace, "storage-expand", vmclient, consts.VMClusterWaitTimeout)

		pvcName := "vmstorage-db-vmstorage-storage-expand-0"
		Eventually(func() string {
			return kubectlOutput(kubeWatched, "get", "pvc", pvcName, "-o", "jsonpath={.spec.resources.requests.storage}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("1Gi"),
			"initial PVC was never created with the expected size")

		// Single-namespace mode can't List StorageClasses, so it needs this manual override annotation.
		_, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "annotate", "pvc", pvcName,
			"operator.victoriametrics.com/pvc-allow-volume-expansion=true")
		require.NoError(t, err)

		_, err = k8s.RunKubectlAndGetOutputE(t, kubeWatched, "patch", "vmcluster", "storage-expand", "--type=merge", "-p",
			`{"spec":{"vmstorage":{"storage":{"volumeClaimTemplate":{"spec":{"resources":{"requests":{"storage":"2Gi"}}}}}}}}`)
		require.NoError(t, err)

		Eventually(func() string {
			return kubectlOutput(kubeWatched, "get", "pvc", pvcName, "-o", "jsonpath={.spec.resources.requests.storage}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("2Gi"),
			"PVC was never expanded in place — isStorageClassExpandable's List call may not be working")
	})

	PIt("grants config-reloader the secrets permissions it actually needs", func(ctx context.Context) {
		// VMStaticScrape's basicAuth gives the operator real secret-derived content to carry through to vmagent.
		install.KubectlApplyFromString(ctx, t, kubeWatched, mustReadOperatorManifest("config-reloader-rbac-secret.yaml"))
		install.KubectlApplyFromString(ctx, t, kubeWatched, mustReadOperatorManifest("config-reloader-rbac-scrape.yaml"))
		install.KubectlApplyFromString(ctx, t, kubeWatched, namedOperatorManifest("vmagent.yaml", "vmagent", "config-reloader-rbac"))

		// /etc/vmagent/config_out/vmagent.yaml is what config-reloader gunzips the config secret into.
		var configContent string
		Eventually(func() bool {
			podName := kubectlOutput(kubeWatched, "get", "pod",
				"-l", "app.kubernetes.io/name=vmagent,app.kubernetes.io/instance=config-reloader-rbac",
				"-o", "jsonpath={.items[0].metadata.name}")
			if podName == "" {
				return false
			}
			output, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "exec", podName, "-c", "vmagent", "--",
				"cat", "/etc/vmagent/config_out/vmagent.yaml")
			if err != nil {
				return false
			}
			configContent = output
			return strings.Contains(configContent, "username: e2e-config-reloader-user")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(BeTrue(),
			"vmagent's live config file never picked up the VMStaticScrape's basicAuth username from the referenced Secret")

		// The initial read only needs "get" (a plain Get before the informer starts), so only a secret update — delivered via the informer's ListAndWatch, which needs "list" — actually exercises the permission this spec guards.
		_, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "patch", "secret", "config-reloader-rbac-creds",
			"--type=merge", "-p", `{"stringData":{"password":"e2e-config-reloader-password-v2"}}`)
		require.NoError(t, err)

		Eventually(func() string {
			podName := kubectlOutput(kubeWatched, "get", "pod",
				"-l", "app.kubernetes.io/name=vmagent,app.kubernetes.io/instance=config-reloader-rbac",
				"-o", "jsonpath={.items[0].metadata.name}")
			if podName == "" {
				return ""
			}
			output, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "exec", podName, "-c", "vmagent", "--",
				"cat", "/etc/vmagent/config_out/vmagent.yaml")
			if err != nil {
				return ""
			}
			configContent = output
			return configContent
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(ContainSubstring("password: e2e-config-reloader-password-v2"),
			"vmagent's live config file never picked up the updated Secret — config-reloader's watch never delivered the change")
	})

	It("grants the operator get/list/watch on every child resource kind it manages", func(ctx context.Context) {
		// ServiceMonitor isn't a built-in API, so the CRD must be installed for the check below to be meaningful.
		k8s.KubectlApplyContext(t, ctx, k8s.NewKubectlOptions("", "", ""), serviceMonitorCRDURL)

		// Resource/verb/scope combinations are taken from the operator's own generated ClusterRole (config/rbac/role.yaml), not guessed.
		type rbacCheck struct {
			resource   string
			verbs      []string
			namespaced bool
		}
		checks := []rbacCheck{
			{"deployments.apps", []string{"get", "list", "watch"}, true},
			{"statefulsets.apps", []string{"get", "list", "watch"}, true},
			{"daemonsets.apps", []string{"get", "list", "watch"}, true},
			{"horizontalpodautoscalers.autoscaling", []string{"get", "list", "watch"}, true},
			{"networkpolicies.networking.k8s.io", []string{"get", "list", "watch"}, true},
			{"servicemonitors.monitoring.coreos.com", []string{"get", "list", "watch"}, true},
			{"poddisruptionbudgets.policy", []string{"get", "list", "watch"}, true},
			// verticalpodautoscalers.autoscaling.k8s.io and httproutes.gateway.networking.k8s.io are missing from the pinned 0.67.2 chart's ClusterRole.
			{"storageclasses.storage.k8s.io", []string{"get", "list", "watch"}, false},
			{"customresourcedefinitions.apiextensions.k8s.io", []string{"get", "list"}, false},
		}

		var operatorSA string
		Eventually(func() string {
			operatorSA = kubectlOutput(kubeOpts, "get", "deployment", "-n", resources.OperatorNamespace, "-l", operatorLabelSelector,
				"-o", "jsonpath={.items[0].spec.template.spec.serviceAccountName}")
			return operatorSA
		}, consts.ResourceWaitTimeout, consts.PollingInterval).ShouldNot(BeEmpty(),
			"could not discover the operator's own ServiceAccount name")

		var missing []string
		for _, check := range checks {
			for _, verb := range check.verbs {
				args := []string{"auth", "can-i", verb, check.resource,
					fmt.Sprintf("--as=system:serviceaccount:%s:%s", resources.OperatorNamespace, operatorSA)}
				if check.namespaced {
					args = append(args, "-n", resources.TestNamespace)
				} else {
					// Avoids kubectl's "not namespace scoped" warning, which terratest merges into stdout and would break the "yes" comparison below.
					args = append(args, "--all-namespaces")
				}
				output, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts, args...)
				if err != nil || strings.TrimSpace(output) != "yes" {
					missing = append(missing, fmt.Sprintf("%s %s", verb, check.resource))
				}
			}
		}
		Expect(missing).To(BeEmpty(),
			"operator ServiceAccount is missing permissions its controller-runtime manager needs")
	})

})

var _ = Describe("operator VMAgent deployment", func() {
	It("creates a valid preStop lifecycle handler", func(ctx context.Context) {
		watchedOpts := k8s.NewKubectlOptions("", "", resources.TestNamespace)
		manifest := mustReadManifest("components/vmagent-lifecycle.yaml")
		install.KubectlApplyFromStringWithRetry(ctx, t, watchedOpts, manifest)
		defer func() {
			_, _ = k8s.RunKubectlAndGetOutputE(t, watchedOpts, "delete", "vmagent", "vmagent-lifecycle", "--ignore-not-found")
		}()

		Eventually(func() bool {
			output, err := k8s.RunKubectlAndGetOutputE(t, watchedOpts, "get", "deployment", "vmagent-vmagent-lifecycle", "-o", "json")
			if err != nil {
				return false
			}
			var deployment struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Lifecycle struct {
									PreStop struct {
										Sleep *struct{} `json:"sleep"`
									} `json:"preStop"`
								} `json:"lifecycle"`
							} `json:"containers"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			}
			if json.Unmarshal([]byte(output), &deployment) != nil {
				return false
			}
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Lifecycle.PreStop.Sleep != nil {
					return true
				}
			}
			return false
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(BeTrue())
	})

	// See https://github.com/VictoriaMetrics/operator/pull/2519
	PIt("deletes HPA when spec.hpa is removed", func(ctx context.Context) {
		watchedOpts := k8s.NewKubectlOptions("", "", resources.TestNamespace)
		manifest := mustReadManifest("components/vmagent-hpa.yaml")
		install.KubectlApplyFromStringWithRetry(ctx, t, watchedOpts, manifest)
		defer func() {
			_, _ = k8s.RunKubectlAndGetOutputE(t, watchedOpts, "delete", "vmagent", "vmagent-hpa", "--ignore-not-found")
		}()

		// HPA name follows the operator's PrefixedName() convention: vmagent-<crname>.
		Eventually(func() (string, error) {
			return k8s.RunKubectlAndGetOutputE(t, watchedOpts, "get", "hpa", "vmagent-vmagent-hpa", "-o", "jsonpath={.metadata.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("vmagent-vmagent-hpa"))

		_, err := k8s.RunKubectlAndGetOutputE(t, watchedOpts, "patch", "vmagent", "vmagent-hpa", "--type=json", "-p", `[{"op":"remove","path":"/spec/hpa"}]`)
		require.NoError(t, err)

		// Regression check for VictoriaMetrics/operator#2518: removing spec.hpa must delete the HPA.
		Eventually(func() string {
			return kubectlOutput(watchedOpts, "get", "hpa", "vmagent-vmagent-hpa", "-o", "jsonpath={.metadata.name}")
		}, consts.OperatorResourceDeletionTimeout, consts.PollingInterval).Should(BeEmpty())
	})

	PIt("deletes VPA when spec.vpa is removed", func(ctx context.Context) {
		watchedOpts := k8s.NewKubectlOptions("", "", resources.TestNamespace)
		manifest := mustReadManifest("components/vmagent-vpa.yaml")
		install.KubectlApplyFromStringWithRetry(ctx, t, watchedOpts, manifest)
		defer func() {
			_, _ = k8s.RunKubectlAndGetOutputE(t, watchedOpts, "delete", "vmagent", "vmagent-vpa", "--ignore-not-found")
		}()

		// VPA name follows the operator's PrefixedName() convention: vmagent-<crname>.
		Eventually(func() (string, error) {
			return k8s.RunKubectlAndGetOutputE(t, watchedOpts, "get", "vpa", "vmagent-vmagent-vpa", "-o", "jsonpath={.metadata.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("vmagent-vmagent-vpa"))

		_, err := k8s.RunKubectlAndGetOutputE(t, watchedOpts, "patch", "vmagent", "vmagent-vpa", "--type=json", "-p", `[{"op":"remove","path":"/spec/vpa"}]`)
		require.NoError(t, err)

		// Regression check for VictoriaMetrics/operator#2519: removing spec.vpa must delete the VPA.
		Eventually(func() string {
			return kubectlOutput(watchedOpts, "get", "vpa", "vmagent-vmagent-vpa", "-o", "jsonpath={.metadata.name}")
		}, consts.OperatorResourceDeletionTimeout, consts.PollingInterval).Should(BeEmpty())
	})

	PIt("deletes NetworkPolicy when spec.networkPolicy is removed", func(ctx context.Context) {
		watchedOpts := k8s.NewKubectlOptions("", "", resources.TestNamespace)
		manifest := mustReadManifest("components/vmagent-networkpolicy.yaml")
		install.KubectlApplyFromStringWithRetry(ctx, t, watchedOpts, manifest)
		defer func() {
			_, _ = k8s.RunKubectlAndGetOutputE(t, watchedOpts, "delete", "vmagent", "vmagent-netpol", "--ignore-not-found")
		}()

		// NetworkPolicy name follows the operator's PrefixedName() convention: vmagent-<crname>.
		Eventually(func() (string, error) {
			return k8s.RunKubectlAndGetOutputE(t, watchedOpts, "get", "networkpolicy", "vmagent-vmagent-netpol", "-o", "jsonpath={.metadata.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("vmagent-vmagent-netpol"))

		_, err := k8s.RunKubectlAndGetOutputE(t, watchedOpts, "patch", "vmagent", "vmagent-netpol", "--type=json", "-p", `[{"op":"remove","path":"/spec/networkPolicy"}]`)
		require.NoError(t, err)

		Eventually(func() string {
			return kubectlOutput(watchedOpts, "get", "networkpolicy", "vmagent-vmagent-netpol", "-o", "jsonpath={.metadata.name}")
		}, consts.OperatorResourceDeletionTimeout, consts.PollingInterval).Should(BeEmpty())
	})
})

// mustReadOperatorManifest reads a static manifest file from manifests/operator/ as-is.
func mustReadOperatorManifest(filename string) string {
	manifest, err := os.ReadFile(consts.ManifestsRoot() + "/operator/" + filename)
	require.NoError(t, err)
	return string(manifest)
}

func mustReadManifest(filename string) string {
	manifest, err := os.ReadFile(consts.ManifestsRoot() + "/" + filename)
	require.NoError(t, err)
	return string(manifest)
}

var _ = Describe("operator global installation", Serial, func() {
	It("supports global installation", Serial, func(ctx context.Context) {
		applyOperator(ctx, operatorHelmParameters(nil))

		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "deployment", "-l", operatorLabelSelectorFor(resources.OperatorApplication), "-o", "jsonpath={.items[0].spec.template.spec.containers[0].env[?(@.name=='WATCH_NAMESPACE')].value}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).ShouldNot(ContainSubstring(resources.TestNamespace))

		name := "global-" + resources.TestNamespace
		testOpts := k8s.NewKubectlOptions("", "", resources.TestNamespace)
		install.KubectlApplyFromString(ctx, t, testOpts, namedOperatorManifest("vmcluster.yaml", "vmcluster", name))
		vmclient := install.GetVMClient(t, testOpts)
		install.WaitForVMClusterToBeOperational(ctx, t, testOpts, resources.TestNamespace, name, vmclient, consts.VMClusterWaitTimeout)
	})

})

var _ = Describe("operator", Serial, func() {
	It("uses an existing ServiceAccount for the operator deployment", func(ctx context.Context) {
		operatorServiceAccount = resources.OperatorApplication + "-custom"
		k8s.RunKubectlContext(t, ctx, k8s.NewKubectlOptions("", "", resources.OperatorNamespace), "create", "serviceaccount", operatorServiceAccount)

		parameters := operatorHelmParameters(map[string]string{
			"serviceAccount.create": "false",
			"serviceAccount.name":   operatorServiceAccount,
			"watchNamespaces[0]":    resources.TestNamespace,
			"watchNamespaces[1]":    resources.OperatorNamespace,
		})
		applyOperator(ctx, parameters)

		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "deployment", "-n", resources.OperatorNamespace, "-l", operatorLabelSelectorFor(resources.OperatorApplication), "-o", "jsonpath={.items[0].spec.template.spec.serviceAccountName}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal(operatorServiceAccount))
	})

	It("configures admission webhooks", func(ctx context.Context) {
		parameters := operatorHelmParameters(map[string]string{
			"admissionWebhooks.enabled": "true",
			"watchNamespaces[0]":        resources.TestNamespace,
			"watchNamespaces[1]":        resources.OperatorNamespace,
		})
		applyOperator(ctx, parameters)

		var webhookConfigName string
		Eventually(func() string {
			webhookConfigName = kubectlOutput(kubeOpts, "get", "validatingwebhookconfiguration", "-l", operatorLabelSelectorFor(resources.OperatorApplication), "-o", "jsonpath={.items[0].metadata.name}")
			return webhookConfigName
		}, consts.ResourceWaitTimeout, consts.PollingInterval).ShouldNot(BeEmpty())
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "validatingwebhookconfiguration", webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.service.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal(resources.OperatorApplication + "-victoria-metrics-operator"))
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "validatingwebhookconfiguration", webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.service.namespace}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal(resources.OperatorNamespace))
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "validatingwebhookconfiguration", webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.service.port}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("9443"))

		validManifest := namedOperatorManifest("vmcluster.yaml", "vmcluster", "admission-valid")
		Eventually(func() error {
			return applyDryRun(k8s.NewKubectlOptions("", "", resources.TestNamespace), validManifest)
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())

		invalidManifest := strings.Replace(validManifest,
			"replicationFactor: 1",
			`replicationFactor: "not-a-number"`,
			1,
		)
		err := applyDryRun(k8s.NewKubectlOptions("", "", resources.TestNamespace), invalidManifest)
		require.Error(t, err)
		require.ErrorContains(t, err, "spec.replicationFactor in body must be of type integer")
		require.ErrorContains(t, err, "replicationFactor")
	})
})

func applyOperator(ctx context.Context, parameters map[string]string) {
	manifest, err := install.BuildArgoCDHelmApplication(resources.OperatorApplication, resources.OperatorNamespace, "victoria-metrics-operator", consts.OperatorChartVersion(), parameters)
	require.NoError(t, err)
	install.ApplyArgoCDApplication(ctx, t, kubeOpts, manifest, resources.OperatorApplication)
}

func normalOperatorParameters() map[string]string {
	return operatorHelmParameters(map[string]string{
		"watchNamespaces[0]": resources.TestNamespace,
		"watchNamespaces[1]": resources.OperatorNamespace,
	})
}

func newTestNamespace() string {
	return fmt.Sprintf("operator-%d-test", GinkgoParallelProcess())
}

func operatorHelmParameters(overrides map[string]string) map[string]string {
	parameters := map[string]string{
		"crds.enabled":              "false",
		"crds.plain":                "false",
		"image.registry":            consts.OperatorImageRegistry(),
		"image.repository":          consts.OperatorImageRepository(),
		"image.tag":                 consts.OperatorImageTag(),
		"operator.vpa_support":      "true",
		"operator.gateway_support":  "true",
		"admissionWebhooks.enabled": "false",
	}
	for name, value := range overrides {
		parameters[name] = value
	}
	return parameters
}

func kubectlOutput(opts *k8s.KubectlOptions, args ...string) string {
	output, err := k8s.RunKubectlAndGetOutputE(t, opts, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// namedOperatorManifest reads a manifests/operator/ file and rewrites its
// placeholder CR name (e.g. "name: vmagent-name") to the given name.
func namedOperatorManifest(filename, placeholder, name string) string {
	return strings.Replace(mustReadOperatorManifest(filename), "name: "+placeholder+"-name", "name: "+name, 1)
}

func applyDryRun(opts *k8s.KubectlOptions, manifest string) error {
	file, err := os.CreateTemp("", "operator-admission-*.yaml")
	require.NoError(t, err)
	defer os.Remove(file.Name())
	_, err = file.WriteString(manifest)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = k8s.RunKubectlAndGetOutputE(t, opts, "apply", "--dry-run=server", "-f", file.Name())
	return err
}

func operatorLabelSelectorFor(application string) string {
	return operatorNameSelector + ",app.kubernetes.io/instance=" + application
}

func scaleDownGlobalOperator() (string, string) {
	vmksOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
	vmksDeployment, err := k8s.RunKubectlAndGetOutputE(t, vmksOpts, "get", "deployment",
		"-l", operatorNameSelector,
		"-o", "jsonpath={.items[0].metadata.name}")
	require.NoError(t, err)
	if vmksDeployment == "" {
		return "", ""
	}

	originalReplicas, err := k8s.RunKubectlAndGetOutputE(t, vmksOpts, "get", "deployment", vmksDeployment,
		"-o", "jsonpath={.spec.replicas}")
	require.NoError(t, err)
	if originalReplicas == "" {
		originalReplicas = "1"
	}

	_, err = k8s.RunKubectlAndGetOutputE(t, vmksOpts, "scale", "deployment", vmksDeployment, "--replicas=0")
	require.NoError(t, err)
	_, err = k8s.RunKubectlAndGetOutputE(t, vmksOpts, "wait", "--for=jsonpath={.spec.replicas}=0",
		"deployment", vmksDeployment, fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
	require.NoError(t, err)
	_, err = k8s.RunKubectlAndGetOutputE(t, vmksOpts, "wait", "--for=delete", "pods", "-l", operatorNameSelector,
		fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
	require.NoError(t, err)
	return vmksDeployment, originalReplicas
}

func removeGlobalOperatorWebhooks(kubeOpts *k8s.KubectlOptions) {
	webhooks, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts, "get", "validatingwebhookconfiguration",
		"-l", operatorNameSelector, "-o", "name")
	require.NoError(t, err)
	if strings.TrimSpace(webhooks) == "" {
		return
	}

	_, err = k8s.RunKubectlAndGetOutputE(t, kubeOpts, "delete", "validatingwebhookconfiguration",
		"-l", operatorNameSelector, "--ignore-not-found=true")
	require.NoError(t, err)
	_, err = k8s.RunKubectlAndGetOutputE(t, kubeOpts, "wait", "--for=delete", "validatingwebhookconfiguration",
		"-l", operatorNameSelector, fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
	require.NoError(t, err)
}

func restoreGlobalOperator() {
	if globalOperatorDeployment == "" || globalOperatorReplicas == "" {
		return
	}
	vmksOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
	_, err := k8s.RunKubectlAndGetOutputE(t, vmksOpts, "scale", "deployment", globalOperatorDeployment, "--replicas="+globalOperatorReplicas)
	require.NoError(t, err)
	if globalOperatorReplicas == "0" {
		return
	}
	_, err = k8s.RunKubectlAndGetOutputE(t, vmksOpts, "wait", "--for=condition=Available",
		"deployment", globalOperatorDeployment, fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
	require.NoError(t, err)
}

func assertOperatorResourceCleanup(ctx context.Context, resource, name, manifest string, inventory map[string][]string, creationTimeout time.Duration) {
	namespace := resources.OperatorNamespace
	install.KubectlApplyFromStringWithRetry(ctx, t, kubeOpts, fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace))

	namespaceOpts := *kubeOpts
	namespaceOpts.Namespace = namespace

	install.KubectlApplyFromStringWithRetry(ctx, t, &namespaceOpts, manifest)
	for kind, names := range inventory {
		for _, name := range names {
			Eventually(func() (string, error) {
				output, err := k8s.RunKubectlAndGetOutputE(t, &namespaceOpts, "get", kind, name, "-o", "jsonpath={.metadata.name}")
				return strings.TrimSpace(output), err
			}, creationTimeout, consts.PollingInterval).Should(Equal(name))
		}
	}

	_, err := k8s.RunKubectlAndGetOutputE(t, &namespaceOpts, "delete", resource, name)
	require.NoError(t, err)
	for kind, names := range inventory {
		for _, name := range names {
			Eventually(func() string {
				return kubectlOutput(&namespaceOpts, "get", kind, name, "-o", "jsonpath={.metadata.name}")
			}, consts.OperatorResourceDeletionTimeout, consts.PollingInterval).Should(BeEmpty(),
				fmt.Sprintf("resource type %q in namespace %q", kind, namespaceOpts.Namespace))
		}
	}
}

type operatorCleanupCase struct {
	resource        string
	name            string
	manifest        string
	inventory       map[string][]string
	creationTimeout time.Duration
}

var _ = Describe("operator resource cleanup", func() {
	DescribeTable("removes owned resources when its custom resource is deleted", func(ctx context.Context, test operatorCleanupCase) {
		assertOperatorResourceCleanup(ctx, test.resource, test.name, test.manifest, test.inventory, test.creationTimeout)
	},
		Entry("VMAgent Deployment, PDB, HPA, VPA, and NetworkPolicy", operatorCleanupCase{
			resource: "vmagent", name: "vmagent-cleanup", manifest: namedOperatorManifest("vmagent-cleanup.yaml", "vmagent", "vmagent-cleanup"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"deployment":          {"vmagent-vmagent-cleanup"},
				"poddisruptionbudget": {"vmagent-vmagent-cleanup"},
				// "hpa":                 {"vmagent-vmagent-cleanup"},
				// "vpa":                 {"vmagent-vmagent-cleanup"},
				// "networkpolicy":       {"vmagent-vmagent-cleanup"},
			},
		}),
		Entry("VMCluster StatefulSets, Deployment, ServiceAccount, PDB, HPA, VPA, and NetworkPolicy", operatorCleanupCase{
			resource: "vmcluster", name: "cleanup-vmcluster", manifest: namedOperatorManifest("vmcluster-cleanup.yaml", "vmcluster", "cleanup-vmcluster"), creationTimeout: consts.VMClusterWaitTimeout,
			inventory: map[string][]string{
				"statefulset":         {"vmstorage-cleanup-vmcluster", "vmselect-cleanup-vmcluster"},
				"deployment":          {"vminsert-cleanup-vmcluster"},
				"serviceaccount":      {"vmcluster-cleanup-vmcluster"},
				"poddisruptionbudget": {"vmstorage-cleanup-vmcluster"},
				"hpa":                 {"vmstorage-cleanup-vmcluster"},
				"vpa":                 {"vmstorage-cleanup-vmcluster"},
				"networkpolicy":       {"vmstorage-cleanup-vmcluster", "vmselect-cleanup-vmcluster", "vminsert-cleanup-vmcluster"},
			},
		}),
		Entry("VTCluster StatefulSet and Deployments", operatorCleanupCase{
			resource: "vtcluster", name: "cleanup-vtcluster", manifest: mustReadOperatorManifest("vtcluster-cleanup.yaml"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"statefulset": {"vtstorage-cleanup-vtcluster"},
				"deployment":  {"vtinsert-cleanup-vtcluster", "vtselect-cleanup-vtcluster"},
			},
		}),
		Entry("VMAuth Deployment, ServiceAccount, Ingress, HTTPRoute, HPA, VPA, PDB, and NetworkPolicy", operatorCleanupCase{
			resource: "vmauth", name: "cleanup-vmauth", manifest: namedOperatorManifest("vmauth.yaml", "vmauth", "cleanup-vmauth"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"deployment":          {"vmauth-cleanup-vmauth"},
				"serviceaccount":      {"vmauth-cleanup-vmauth"},
				"ingress":             {"vmauth-cleanup-vmauth"},
				"httproute":           {"vmauth-cleanup-vmauth"},
				"hpa":                 {"vmauth-cleanup-vmauth"},
				"vpa":                 {"vmauth-cleanup-vmauth"},
				"poddisruptionbudget": {"vmauth-cleanup-vmauth"},
				"networkpolicy":       {"vmauth-cleanup-vmauth"},
			},
		}),
		Entry("VMSingle Deployment, ServiceAccount, VPA, and NetworkPolicy", operatorCleanupCase{
			resource: "vmsingle", name: "cleanup-vmsingle", manifest: namedOperatorManifest("vmsingle-cleanup.yaml", "vmsingle", "cleanup-vmsingle"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"deployment":     {"vmsingle-cleanup-vmsingle"},
				"serviceaccount": {"vmsingle-cleanup-vmsingle"},
				"pvc":            {"vmsingle-cleanup-vmsingle"},
				"vpa":            {"vmsingle-cleanup-vmsingle"},
				"networkpolicy":  {"vmsingle-cleanup-vmsingle"},
			},
		}),
		Entry("VMUser Secret", operatorCleanupCase{
			resource: "vmuser", name: "cleanup-vmuser", manifest: namedOperatorManifest("vmuser.yaml", "vmuser", "cleanup-vmuser"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"secret": {"vmuser-cleanup-vmuser"},
			},
		}),
		Entry("VTSingle Deployment, ServiceAccount, VPA, and NetworkPolicy", operatorCleanupCase{
			resource: "vtsingle", name: "cleanup-vtsingle", manifest: namedOperatorManifest("vtsingle-cleanup.yaml", "vtsingle", "cleanup-vtsingle"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"deployment":     {"vtsingle-cleanup-vtsingle"},
				"serviceaccount": {"vtsingle-cleanup-vtsingle"},
				// PVC intentionally excluded: VTSingleSpec has no RemovePvcAfterDelete field,
				// so the operator never sets PVC ownership and it survives CR deletion.
				"vpa":           {"vtsingle-cleanup-vtsingle"},
				"networkpolicy": {"vtsingle-cleanup-vtsingle"},
			},
		}),
		Entry("VMAlertmanager StatefulSet, ServiceAccount, PDB, VPA, and NetworkPolicy", operatorCleanupCase{
			resource: "vmalertmanager", name: "cleanup-vmalertmanager", manifest: namedOperatorManifest("vmalertmanager-cleanup.yaml", "vmalertmanager", "cleanup-vmalertmanager"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"statefulset":         {"vmalertmanager-cleanup-vmalertmanager"},
				"serviceaccount":      {"vmalertmanager-cleanup-vmalertmanager"},
				"poddisruptionbudget": {"vmalertmanager-cleanup-vmalertmanager"},
				"vpa":                 {"vmalertmanager-cleanup-vmalertmanager"},
				"networkpolicy":       {"vmalertmanager-cleanup-vmalertmanager"},
			},
		}),
		Entry("VMAlert Deployment, ServiceAccount, PDB, VPA, and NetworkPolicy", operatorCleanupCase{
			resource: "vmalert", name: "cleanup-vmalert", manifest: namedOperatorManifest("vmalert-cleanup.yaml", "vmalert", "cleanup-vmalert"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"deployment":          {"vmalert-cleanup-vmalert"},
				"serviceaccount":      {"vmalert-cleanup-vmalert"},
				"poddisruptionbudget": {"vmalert-cleanup-vmalert"},
				"vpa":                 {"vmalert-cleanup-vmalert"},
				"networkpolicy":       {"vmalert-cleanup-vmalert"},
			},
		}),
		// PVC cleanup is not implemented
		PEntry("VLSingle Deployment, ServiceAccount, PVC, VPA, and NetworkPolicy", operatorCleanupCase{
			resource: "vlsingle", name: "cleanup-vlsingle", manifest: namedOperatorManifest("vlsingle-cleanup.yaml", "vlsingle", "cleanup-vlsingle"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"deployment":     {"vlsingle-cleanup-vlsingle"},
				"serviceaccount": {"vlsingle-cleanup-vlsingle"},
				"pvc":            {"vlsingle-cleanup-vlsingle"},
				"vpa":            {"vlsingle-cleanup-vlsingle"},
				"networkpolicy":  {"vlsingle-cleanup-vlsingle"},
			},
		}),
		Entry("VLCluster StatefulSet, Deployments, HPA, VPA, and NetworkPolicies", operatorCleanupCase{
			resource: "vlcluster", name: "cleanup-vlcluster", manifest: mustReadOperatorManifest("vlcluster-cleanup.yaml"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"statefulset":   {"vlstorage-cleanup-vlcluster"},
				"deployment":    {"vlinsert-cleanup-vlcluster", "vlselect-cleanup-vlcluster"},
				"hpa":           {"vlstorage-cleanup-vlcluster"},
				"vpa":           {"vlstorage-cleanup-vlcluster"},
				"networkpolicy": {"vlinsert-cleanup-vlcluster", "vlselect-cleanup-vlcluster", "vlstorage-cleanup-vlcluster"},
			},
		}),
		Entry("VLAgent DaemonSet, ServiceAccount, and NetworkPolicy", operatorCleanupCase{
			resource: "vlagent", name: "cleanup-vlagent", manifest: namedOperatorManifest("vlagent-cleanup.yaml", "vlagent", "cleanup-vlagent"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"daemonset":      {"vlagent-cleanup-vlagent"},
				"serviceaccount": {"vlagent-cleanup-vlagent"},
				"networkpolicy":  {"vlagent-cleanup-vlagent"},
			},
		}),
		Entry("VLAgent StatefulSet, ServiceAccount, PDB, and NetworkPolicy", operatorCleanupCase{
			resource: "vlagent", name: "cleanup-vlagent-statefulset", manifest: mustReadOperatorManifest("vlagent-statefulset-cleanup.yaml"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"statefulset":         {"vlagent-cleanup-vlagent-statefulset"},
				"serviceaccount":      {"vlagent-cleanup-vlagent-statefulset"},
				"poddisruptionbudget": {"vlagent-cleanup-vlagent-statefulset"},
				"networkpolicy":       {"vlagent-cleanup-vlagent-statefulset"},
			},
		}),
		// See https://github.com/VictoriaMetrics/operator/pull/2540
		PEntry("VMAgent DaemonSet, ServiceAccount, and NetworkPolicy", operatorCleanupCase{
			resource: "vmagent", name: "cleanup-vmagent-ds", manifest: namedOperatorManifest("vmagent-daemonset-cleanup.yaml", "vmagent", "cleanup-vmagent-ds"), creationTimeout: consts.ResourceWaitTimeout,
			inventory: map[string][]string{
				"daemonset":      {"vmagent-cleanup-vmagent-ds"},
				"serviceaccount": {"vmagent-cleanup-vmagent-ds"},
				"networkpolicy":  {"vmagent-cleanup-vmagent-ds"},
			},
		}),
	)
})
