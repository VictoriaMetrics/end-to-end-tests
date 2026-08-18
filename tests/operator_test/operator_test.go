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
	operatorTestLabel    = "operator-test=true"

	// serviceMonitorCRDURL is prometheus-operator's own published ServiceMonitor CRD.
	serviceMonitorCRDURL = "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.79.2/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml"
)

var (
	t                        terratesting.TestingT
	kubeOpts                 *k8s.KubectlOptions
	appManifest              string
	operatorNamespace        string
	customNamespace          string
	globalNamespace          string
	webhookNamespace         string
	watchedNamespace         string
	unwatchedNamespace       string
	operatorServiceAccount   string
	operatorApplication      string
	customApplication        string
	globalApplication        string
	webhookApplication       string
	globalOperatorDeployment string
	globalOperatorReplicas   string
	operatorLabelSelector    string
	suiteStartTime           time.Time
)

type suiteResources struct {
	OperatorNamespace        string `json:"operatorNamespace"`
	CustomNamespace          string `json:"customNamespace"`
	GlobalNamespace          string `json:"globalNamespace"`
	WebhookNamespace         string `json:"webhookNamespace"`
	WatchedNamespace         string `json:"watchedNamespace"`
	UnwatchedNamespace       string `json:"unwatchedNamespace"`
	OperatorServiceAccount   string `json:"operatorServiceAccount"`
	OperatorApplication      string `json:"operatorApplication"`
	CustomApplication        string `json:"customApplication"`
	GlobalApplication        string `json:"globalApplication"`
	WebhookApplication       string `json:"webhookApplication"`
	GlobalOperatorDeployment string `json:"globalOperatorDeployment"`
	GlobalOperatorReplicas   string `json:"globalOperatorReplicas"`
}

func TestOperatorHelmSuite(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	RunSpecs(t, "Operator Helm Suite")
}

var _ = SynchronizedBeforeSuite(func(ctx context.Context) []byte {
	t = tests.GetT()
	suiteStartTime = time.Now()
	resources := suiteResources{
		OperatorNamespace:      tests.RandomNamespace("operator-system"),
		CustomNamespace:        tests.RandomNamespace("operator-custom"),
		GlobalNamespace:        tests.RandomNamespace("operator-global"),
		WebhookNamespace:       tests.RandomNamespace("operator-webhook"),
		WatchedNamespace:       tests.RandomNamespace("operator-watched"),
		UnwatchedNamespace:     tests.RandomNamespace("operator-unwatched"),
		OperatorServiceAccount: tests.RandomNamespace("vm-operator-e2e"),
		OperatorApplication:    tests.RandomNamespace("vm-operator-e2e"),
		CustomApplication:      tests.RandomNamespace("vm-operator-e2e-custom"),
		GlobalApplication:      tests.RandomNamespace("vm-operator-e2e-global"),
		WebhookApplication:     tests.RandomNamespace("vm-operator-e2e-webhook"),
	}
	setSuiteResources(resources)
	kubeOpts = k8s.NewKubectlOptions("", "", "default")

	removeGlobalOperatorWebhooks(kubeOpts)
	tests.CleanupStaleNamespaces(ctx, t, kubeOpts, operatorTestLabel)
	install.EnsureVPACRDs(ctx, t, kubeOpts)
	install.EnsureGatewayAPICRDs(ctx, t, kubeOpts)
	install.DiscoverIngressHost(ctx, t)
	tests.InstallVMStackAndGather(ctx, t)
	tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})
	resources.GlobalOperatorDeployment, resources.GlobalOperatorReplicas = scaleDownGlobalOperator()
	install.InstallArgoCD(ctx, t, kubeOpts, consts.ArgoCDVersion())

	parameters := operatorHelmParameters(map[string]string{
		"watchNamespaces[0]": watchedNamespace,
		"crds.enabled":       "true",
	})
	var err error
	appManifest, err = install.BuildArgoCDHelmApplication(operatorApplication, operatorNamespace, "victoria-metrics-operator", consts.OperatorChartVersion(), parameters)
	require.NoError(t, err)

	for _, namespace := range []string{operatorNamespace, customNamespace, globalNamespace, webhookNamespace, watchedNamespace, unwatchedNamespace} {
		_, err = k8s.RunKubectlAndGetOutputE(t, kubeOpts, "create", "namespace", namespace)
		require.NoError(t, err)
		_, err = k8s.RunKubectlAndGetOutputE(t, kubeOpts, "wait", "--for=jsonpath={.status.phase}=Active", "namespace", namespace, fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
		require.NoError(t, err)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, operatorTestLabel, "--overwrite")
	}
	install.ApplyArgoCDApplication(ctx, t, kubeOpts, appManifest, operatorApplication)
	kubeOpts = k8s.NewKubectlOptions("", "", operatorNamespace)
	data, err := json.Marshal(resources)
	require.NoError(t, err)
	return data
}, func(ctx context.Context, data []byte) {
	t = tests.GetT()
	var resources suiteResources
	require.NoError(t, json.Unmarshal(data, &resources))
	setSuiteResources(resources)
	kubeOpts = k8s.NewKubectlOptions("", "", operatorNamespace)
})

var _ = SynchronizedAfterSuite(func(ctx context.Context) {
	if kubeOpts != nil {
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, operatorNamespace, suiteStartTime)
	}
}, func(ctx context.Context) {
	if kubeOpts == nil {
		restoreGlobalOperator()
		return
	}
	deleteOperatorVMClusters(ctx)
	for _, application := range []string{operatorApplication, customApplication, globalApplication, webhookApplication} {
		install.DeleteArgoCDApplication(t, kubeOpts, application)
	}
	k8s.RunKubectlContext(t, ctx, kubeOpts, "delete", "validatingwebhookconfiguration", "-l", operatorLabelSelector, "--ignore-not-found=true")
	for _, namespace := range []string{watchedNamespace, unwatchedNamespace, operatorNamespace, customNamespace, globalNamespace, webhookNamespace} {
		k8s.RunKubectlContext(t, ctx, kubeOpts, "delete", "namespace", namespace, "--ignore-not-found=true", "--wait=true", fmt.Sprintf("--timeout=%s", consts.PollingTimeout))
	}
	restoreGlobalOperator()
})

var _ = Describe("operator Helm deployment", func() {
	It("limits reconciliation to WATCH_NAMESPACE", func(ctx context.Context) {
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "deployment", "-n", operatorNamespace, "-l", operatorLabelSelector, "-o", "jsonpath={.items[0].spec.template.spec.containers[0].env[?(@.name=='WATCH_NAMESPACE')].value}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(ContainSubstring(watchedNamespace))

		install.KubectlApplyFromString(ctx, t, k8s.NewKubectlOptions("", "", watchedNamespace), vmClusterManifest("watched"))
		install.KubectlApplyFromString(ctx, t, k8s.NewKubectlOptions("", "", unwatchedNamespace), vmClusterManifest("unwatched"))

		vmclient := install.GetVMClient(t, kubeOpts)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeOpts, watchedNamespace, vmclient, consts.VMClusterWaitTimeout)
		Consistently(func() string {
			return kubectlOutput(kubeOpts, "get", "vmcluster", "unwatched", "-n", unwatchedNamespace, "-o", "jsonpath={.status.updateStatus}")
		}, 30*time.Second, consts.PollingInterval).Should(BeEmpty())
	})

	It("watches updates and removes owned resources", func(ctx context.Context) {
		kubeWatched := k8s.NewKubectlOptions("", "", watchedNamespace)
		install.KubectlApplyFromString(ctx, t, kubeWatched, vmClusterManifest("lifecycle"))
		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, watchedNamespace, vmclient, consts.VMClusterWaitTimeout)
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "statefulset", "vmstorage-lifecycle", "-n", watchedNamespace, "-o", "jsonpath={.metadata.name}")
		}, consts.VMClusterWaitTimeout, consts.PollingInterval).Should(Equal("vmstorage-lifecycle"))

		_, err := k8s.RunKubectlAndGetOutputE(t, kubeWatched, "patch", "vmcluster", "lifecycle", "--type=merge", "-p", `{"spec":{"retentionPeriod":"2d","vmstorage":{"replicaCount":2}}}`)
		require.NoError(t, err)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, watchedNamespace, vmclient, consts.VMClusterWaitTimeout)
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "statefulset", "vmstorage-lifecycle", "-n", watchedNamespace, "-o", "jsonpath={.spec.replicas}")
		}, consts.VMClusterWaitTimeout, consts.PollingInterval).Should(Equal("2"))

		k8s.RunKubectlContext(t, ctx, kubeWatched, "delete", "vmcluster", "lifecycle")
		Eventually(func() string {
			return kubectlOutput(kubeOpts, "get", "statefulset", "vmstorage-lifecycle", "-n", watchedNamespace, "-o", "jsonpath={.metadata.name}")
		}, consts.VMClusterWaitTimeout, consts.PollingInterval).Should(BeEmpty())
	})

	It("cleans up the auto-created ServiceAccount when switching to a custom one", func(ctx context.Context) {
		// Switching serviceAccountName used to leave the auto-created ServiceAccount orphaned (victoriametrics/operator#1665).
		kubeWatched := k8s.NewKubectlOptions("", "", watchedNamespace)
		install.KubectlApplyFromString(ctx, t, kubeWatched, vmClusterManifest("sa-switch"))
		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, watchedNamespace, vmclient, consts.VMClusterWaitTimeout)

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
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, watchedNamespace, vmclient, consts.VMClusterWaitTimeout)

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
		kubeWatched := k8s.NewKubectlOptions("", "", watchedNamespace)
		install.KubectlApplyFromString(ctx, t, kubeWatched, vmClusterManifest("lb-class"))
		vmclient := install.GetVMClient(t, kubeWatched)
		install.WaitForVMClusterToBeOperational(ctx, t, kubeWatched, watchedNamespace, vmclient, consts.VMClusterWaitTimeout)

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

	It("grants config-reloader the secrets permissions it actually needs", func(ctx context.Context) {
		kubeWatched := k8s.NewKubectlOptions("", "", watchedNamespace)

		// A VMStaticScrape with a basicAuth secret reference, so the
		// operator's generated scrape config has real secret-derived content
		// to carry through to the running vmagent — not just an empty
		// VMAgent, which wouldn't exercise the secret data path at all. If
		// config-reloader can't read its config secret (the RBAC regression
		// this spec guards, victoriametrics/operator#2384), vmagent's own
		// config file never gets written and it never starts serving
		// /config, so this fails clearly instead of a bare timeout.
		install.KubectlApplyFromString(ctx, t, kubeWatched, mustReadOperatorManifest("config-reloader-rbac-secret.yaml"))
		install.KubectlApplyFromString(ctx, t, kubeWatched, mustReadOperatorManifest("config-reloader-rbac-scrape.yaml"))
		install.KubectlApplyFromString(ctx, t, kubeWatched, vmAgentManifest("config-reloader-rbac"))

		// Read the actual file vmagent runs with — config-reloader gunzips
		// the operator-written config secret straight into
		// /etc/vmagent/config_out/vmagent.yaml (see
		// VictoriaMetrics/operator's vmagent.go: confOutDir + configFilename,
		// the exact path passed to vmagent's own -promscrape.config flag) —
		// rather than the secret itself, so this exercises config-reloader's
		// read/write path end to end rather than just what the operator
		// produced. If config-reloader can't read its config secret (the
		// RBAC regression this spec guards, victoriametrics/operator#2384),
		// this file never gets written and cat fails, so this fails clearly
		// instead of a bare timeout.
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

		// Unlike vmagent's /config HTTP endpoint (which redacts basic_auth
		// passwords to the literal "<secret>"), this is the raw file
		// config-reloader wrote — a plain gunzip of the config secret with no
		// re-marshaling — so the real password is readable here.
		Expect(configContent).To(ContainSubstring("password: e2e-config-reloader-password"),
			"vmagent's live config file is missing the basicAuth password from the referenced Secret")
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
			{"verticalpodautoscalers.autoscaling.k8s.io", []string{"get", "list", "watch"}, true},
			{"networkpolicies.networking.k8s.io", []string{"get", "list", "watch"}, true},
			{"servicemonitors.monitoring.coreos.com", []string{"get", "list", "watch"}, true},
			{"httproutes.gateway.networking.k8s.io", []string{"get", "list", "watch"}, true},
			{"poddisruptionbudgets.policy", []string{"get", "list", "watch"}, true},
			{"storageclasses.storage.k8s.io", []string{"get", "list", "watch"}, false},
			{"customresourcedefinitions.apiextensions.k8s.io", []string{"get", "list"}, false},
		}

		var operatorSA string
		Eventually(func() string {
			operatorSA = kubectlOutput(kubeOpts, "get", "deployment", "-n", operatorNamespace, "-l", operatorLabelSelector,
				"-o", "jsonpath={.items[0].spec.template.spec.serviceAccountName}")
			return operatorSA
		}, consts.ResourceWaitTimeout, consts.PollingInterval).ShouldNot(BeEmpty(),
			"could not discover the operator's own ServiceAccount name")

		var missing []string
		for _, check := range checks {
			for _, verb := range check.verbs {
				args := []string{"auth", "can-i", verb, check.resource,
					fmt.Sprintf("--as=system:serviceaccount:%s:%s", operatorNamespace, operatorSA)}
				if check.namespaced {
					args = append(args, "-n", watchedNamespace)
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

// mustReadOperatorManifest reads a static manifest file from manifests/operator/ as-is.
func mustReadOperatorManifest(filename string) string {
	manifest, err := os.ReadFile(consts.ManifestsRoot() + "/operator/" + filename)
	require.NoError(t, err)
	return string(manifest)
}

var _ = Describe("operator global installation", Serial, func() {
	It("supports global installation", Serial, func(ctx context.Context) {
		globalOpts := k8s.NewKubectlOptions("", "", globalNamespace)

		parameters := operatorHelmParameters(nil)
		manifest, err := install.BuildArgoCDHelmApplication(globalApplication, globalNamespace, "victoria-metrics-operator", consts.OperatorChartVersion(), parameters)
		require.NoError(t, err)
		install.ApplyArgoCDApplication(ctx, t, globalOpts, manifest, globalApplication)

		Eventually(func() string {
			return kubectlOutput(globalOpts, "get", "deployment", "-l", operatorLabelSelectorFor(globalApplication), "-o", "jsonpath={.items[0].spec.template.spec.containers[0].env[?(@.name=='WATCH_NAMESPACE')].value}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).ShouldNot(ContainSubstring(watchedNamespace))

		for _, namespace := range []string{watchedNamespace, unwatchedNamespace} {
			name := "global-" + namespace
			namespaceOpts := k8s.NewKubectlOptions("", "", namespace)
			install.KubectlApplyFromString(ctx, t, namespaceOpts, vmClusterManifest(name))
			vmclient := install.GetVMClient(t, namespaceOpts)
			install.WaitForVMClusterToBeOperational(ctx, t, namespaceOpts, namespace, vmclient, consts.VMClusterWaitTimeout)
		}
	})

})

var _ = Describe("operator Helm custom ServiceAccount", Serial, func() {
	It("uses an existing ServiceAccount for the operator deployment", func(ctx context.Context) {
		customOpts := k8s.NewKubectlOptions("", "", customNamespace)
		k8s.RunKubectlContext(t, ctx, customOpts, "create", "serviceaccount", operatorServiceAccount)

		parameters := operatorHelmParameters(map[string]string{
			"serviceAccount.create": "false",
			"serviceAccount.name":   operatorServiceAccount,
			"watchNamespaces[0]":    customNamespace,
		})
		manifest, err := install.BuildArgoCDHelmApplication(customApplication, customNamespace, "victoria-metrics-operator", consts.OperatorChartVersion(), parameters)
		require.NoError(t, err)
		install.ApplyArgoCDApplication(ctx, t, customOpts, manifest, customApplication)

		Eventually(func() string {
			return kubectlOutput(customOpts, "get", "deployment", "-l", operatorLabelSelectorFor(customApplication), "-o", "jsonpath={.items[0].spec.template.spec.serviceAccountName}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal(operatorServiceAccount))
	})
})

var _ = Describe("operator Helm admission webhooks", Serial, func() {
	It("configures admission webhooks", func(ctx context.Context) {
		webhookOpts := k8s.NewKubectlOptions("", "", webhookNamespace)
		parameters := operatorHelmParameters(map[string]string{
			"admissionWebhooks.enabled": "true",
			"watchNamespaces[0]":        webhookNamespace,
		})
		manifest, err := install.BuildArgoCDHelmApplication(webhookApplication, webhookNamespace, "victoria-metrics-operator", consts.OperatorChartVersion(), parameters)
		require.NoError(t, err)
		install.ApplyArgoCDApplication(ctx, t, webhookOpts, manifest, webhookApplication)

		var webhookConfigName string
		Eventually(func() string {
			webhookConfigName = kubectlOutput(webhookOpts, "get", "validatingwebhookconfiguration", "-l", operatorLabelSelectorFor(webhookApplication), "-o", "jsonpath={.items[0].metadata.name}")
			return webhookConfigName
		}, consts.ResourceWaitTimeout, consts.PollingInterval).ShouldNot(BeEmpty())
		Eventually(func() string {
			return kubectlOutput(webhookOpts, "get", "validatingwebhookconfiguration", webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.service.name}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal(webhookApplication + "-victoria-metrics-operator"))
		Eventually(func() string {
			return kubectlOutput(webhookOpts, "get", "validatingwebhookconfiguration", webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.service.namespace}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal(webhookNamespace))
		Eventually(func() string {
			return kubectlOutput(webhookOpts, "get", "validatingwebhookconfiguration", webhookConfigName, "-o", "jsonpath={.webhooks[0].clientConfig.service.port}")
		}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Equal("9443"))

		validManifest := vmClusterManifest("admission-valid")
		require.NoError(t, applyDryRun(webhookOpts, validManifest))

		invalidManifest := strings.Replace(validManifest,
			"replicationFactor: 1",
			`replicationFactor: "not-a-number"`,
			1,
		)
		err = applyDryRun(webhookOpts, invalidManifest)
		require.Error(t, err)
		require.ErrorContains(t, err, "cannot parse VMClusterSpec")
		require.ErrorContains(t, err, "replicationFactor")
	})
})

func operatorHelmParameters(overrides map[string]string) map[string]string {
	parameters := map[string]string{
		"image.registry":            consts.OperatorImageRegistry(),
		"image.repository":          consts.OperatorImageRepository(),
		"image.tag":                 consts.OperatorImageTag(),
		"admissionWebhooks.enabled": "false",
		"crds.enabled":              "false",
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

func vmClusterManifest(name string) string {
	manifest, err := os.ReadFile(consts.ManifestsRoot() + "/operator/vmcluster.yaml")
	require.NoError(t, err)
	return strings.Replace(string(manifest), "name: vmcluster-name", "name: "+name, 1)
}

func vmAgentManifest(name string) string {
	manifest, err := os.ReadFile(consts.ManifestsRoot() + "/operator/vmagent.yaml")
	require.NoError(t, err)
	return strings.Replace(string(manifest), "name: vmagent-name", "name: "+name, 1)
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

func setSuiteResources(resources suiteResources) {
	operatorNamespace = resources.OperatorNamespace
	customNamespace = resources.CustomNamespace
	globalNamespace = resources.GlobalNamespace
	webhookNamespace = resources.WebhookNamespace
	watchedNamespace = resources.WatchedNamespace
	unwatchedNamespace = resources.UnwatchedNamespace
	operatorServiceAccount = resources.OperatorServiceAccount
	customApplication = resources.CustomApplication
	globalApplication = resources.GlobalApplication
	webhookApplication = resources.WebhookApplication
	globalOperatorDeployment = resources.GlobalOperatorDeployment
	globalOperatorReplicas = resources.GlobalOperatorReplicas
	operatorApplication = resources.OperatorApplication
	operatorLabelSelector = operatorLabelSelectorFor(operatorApplication)
}

func operatorLabelSelectorFor(application string) string {
	return operatorNameSelector + ",app.kubernetes.io/instance=" + application
}

func deleteOperatorVMClusters(ctx context.Context) {
	for _, namespace := range []string{watchedNamespace, unwatchedNamespace} {
		namespaceOpts := k8s.NewKubectlOptions("", "", namespace)
		k8s.RunKubectlContext(t, ctx, namespaceOpts, "delete", "vmcluster", "--all", "--ignore-not-found=true", "--wait=true", fmt.Sprintf("--timeout=%s", consts.PollingTimeout))
	}
}

func scaleDownGlobalOperator() (string, string) {
	vmksOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
	removeGlobalOperatorWebhooks(vmksOpts)
	vmksDeployment, err := k8s.RunKubectlAndGetOutputE(t, vmksOpts, "get", "deployment",
		"-l", operatorNameSelector, "-o", "jsonpath={.items[0].metadata.name}")
	require.NoError(t, err)
	require.NotEmpty(t, vmksDeployment)

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
