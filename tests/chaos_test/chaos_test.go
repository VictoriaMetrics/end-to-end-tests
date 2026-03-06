package chaos_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	jsonpatch "github.com/evanphx/json-patch/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestChaosTestsTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Chaos test Suite", suiteConfig, reporterConfig)
}

var (
	t terratesting.TestingT
)

// Install VM from helm chart for the first process, set namespace for the rest
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t := tests.GetT()
		install.DiscoverIngressHost(ctx, t)

		// Install Chaos Mesh
		chaosCfg := tests.DefaultChaosMeshConfig()
		install.InstallChaosMesh(
			ctx,
			chaosCfg.HelmChart,
			chaosCfg.ValuesFile,
			t,
			chaosCfg.Namespace,
			chaosCfg.ReleaseName,
		)

		// Install VM K8s Stack
		install.InstallVMGather(t)
		install.InstallVMK8StackWithHelm(
			context.Background(),
			consts.VMK8sStackChart,
			consts.SmokeValuesFile,
			t,
			consts.DefaultVMNamespace,
			consts.DefaultReleaseName,
		)
		install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)

		// Remove stock VMCluster - it would be recreated in vm* namespaces
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.DeleteVMCluster(t, kubeOpts, consts.DefaultReleaseName)

		// Add custom alert rules
		install.AddCustomAlertRules(ctx, t, consts.DefaultVMNamespace)
	}, func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = Describe("Chaos tests", Label("chaos-test"), func() {

	// ChaosScenario represents a chaos test scenario configuration
	type ChaosScenario struct {
		UUID         string
		ScenarioName string
		Category     string
		ChaosType    string
		CheckAlerts  []string
	}

	// Helper function to run a chaos scenario
	runChaosScenario := func(ctx context.Context, scenario ChaosScenario) {
		overwatch, err := tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)

		namespace := fmt.Sprintf("vm-%s", scenario.ScenarioName)
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)

		tests.CleanupNamespace(t, kubeOpts, namespace)
		tests.EnsureNamespaceExists(t, kubeOpts, namespace)

		defer func() {
			tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)
			install.DeleteVMCluster(t, kubeOpts, namespace)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		}()

		overwatch.CheckNoAlertsFiring(ctx, t, namespace, promquery.DefaultExceptions)

		// Create new VMCluster object
		vmclient := install.GetVMClient(t, kubeOpts)

		clusterName := namespace
		affinity := map[string]interface{}{
			"podAntiAffinity": map[string]interface{}{
				"requiredDuringSchedulingIgnoredDuringExecution": []map[string]interface{}{
					{
						"topologyKey": "kubernetes.io/hostname",
						"labelSelector": map[string]interface{}{
							"matchExpressions": []map[string]interface{}{
								{
									"key":      "app.kubernetes.io/instance",
									"operator": "Exists",
								},
								{
									"key":      "app.kubernetes.io/instance",
									"operator": "NotIn",
									"values":   []string{clusterName},
								},
							},
						},
					},
				},
			},
		}

		patches := []jsonpatch.Patch{}
		for _, component := range []string{"vminsert", "vmselect", "vmstorage"} {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/metadata/name", clusterName).
				Add(fmt.Sprintf("/spec/%s/affinity", component), affinity).
				MustBuild())
		}

		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, patches)
		By("VMCluster is available")

		// Ensure VMAgent remote write URL is set up
		remoteWriteURL := fmt.Sprintf(
			"http://vminsert-vm.%s.svc.cluster.local.:8480/insert/0/prometheus/api/v1/write",
			namespace)
		logger.Default.Logf(t, "Setting vmagent remote write URL to %s", remoteWriteURL)
		install.EnsureVMAgentRemoteWriteURL(ctx, t, vmclient, kubeOpts, consts.DefaultVMNamespace, consts.DefaultReleaseName, remoteWriteURL)

		By(fmt.Sprintf("Running %s scenario", scenario.ScenarioName))
		install.RunChaosScenario(ctx, t, namespace, scenario.Category, scenario.ScenarioName, scenario.ChaosType)

		if len(scenario.CheckAlerts) > 0 {
			for _, alert := range scenario.CheckAlerts {
				By(fmt.Sprintf("Alert %s is firing", alert))
				overwatch.CheckAlertIsFiring(ctx, t, namespace, alert)
			}
		} else {
			By("No alerts are firing")
			overwatch.CheckNoAlertsFiring(ctx, t, namespace, nil)
		}
	}

	Describe("pod restarts", Label("kind", "chaos-pod-failure"), func() {
		DescribeTable("should handle pod failure scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert pod failure",
				Label("id=17f2e31b-9249-4283-845b-aae0bc81e5f2"),
				ChaosScenario{
					ScenarioName: "vminsert-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
					CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmstorage pod failure",
				Label("id=e340d25f-b14f-4f21-acb4-68c4fdf39a85"),
				ChaosScenario{
					ScenarioName: "vmstorage-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
					CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmselect pod failure",
				Label("id=38df1d4b-d38c-4064-8538-c0e03920255f"),
				ChaosScenario{
					ScenarioName: "vmselect-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
					CheckAlerts:  []string{"ServiceDown"},
				},
			),
		)
	})

	Describe("cpu stress", Serial, Label("kind", "chaos-cpu-stress"), func() {
		DescribeTable("should handle CPU stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert CPU stress",
				Label("id=4c571bca-2442-4a1b-8e54-4f9878f8dd6d"),
				ChaosScenario{
					ScenarioName: "vminsert-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomCPUThrottlingHigh"},
				},
			),
			Entry("vmstorage CPU stress",
				Label("id=d1ebdfd3-a0cf-4525-89b9-e998ec7b0c1e"),
				ChaosScenario{
					ScenarioName: "vmstorage-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomCPUThrottlingHigh"},
				},
			),
			Entry("vmselect CPU stress",
				Label("id=f6637d83-be2a-44ab-b446-9c755bad4292"),
				ChaosScenario{
					ScenarioName: "vmselect-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomCPUThrottlingHigh"},
				},
			),
		)
	})

	Describe("memory stress", Label("kind", "chaos-memory-stress"), func() {
		DescribeTable("should handle memory stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert memory stress",
				Label("id=47690837-45e5-4cae-9e60-abadf59e4e66"),
				ChaosScenario{
					ScenarioName: "vminsert-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
					// CheckAlerts:  []string{"CustomHighMemoryUsage"},
				},
			),
			Entry("vmstorage memory stress",
				Label("id=357cef7e-c2ce-4a76-8768-7b142a4e7997"),
				ChaosScenario{
					ScenarioName: "vmstorage-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
					// CheckAlerts:  []string{"CustomHighMemoryUsage"},
				},
			),
			Entry("vmselect memory stress",
				Label("id=f9c922b8-104a-4baf-bad3-b00188ccddb1"),
				ChaosScenario{
					ScenarioName: "vmselect-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
					// CheckAlerts:  []string{"CustomHighMemoryUsage"},
				},
			),
		)
	})

	// This needs to run on serial so that the IO stress scenarios do not interfere with each other.
	Describe("io stress", Serial, Label("kind", "chaos-io-stress"), func() {
		DescribeTable("should handle IO stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert IO stress",
				Label("id=c70ce6cc-84fe-447d-8b5f-48871a2ebf99"),
				ChaosScenario{
					ScenarioName: "vminsert-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vmstorage IO stress",
				Label("id=8b3f1e4a-2c5d-4f67-9aab-123456abcdef"),
				ChaosScenario{
					ScenarioName: "vmstorage-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmselect IO stress",
				Label("id=9c4d2b3a-1f0e-4d6c-8b7a-abcdef123456"),
				ChaosScenario{
					UUID:         "9c4d2b3a-1f0e-4d6c-8b7a-abcdef123456",
					ScenarioName: "vmselect-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
		)
	})

	Describe("network failure", Label("kind", "chaos-network-failure"), func() {
		DescribeTable("should handle network chaos scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert to vmstorage packet corrupt",
				Label("id=ef3455cd-7687-49a4-b423-7c4541aa051c"),
				ChaosScenario{
					ScenarioName: "vminsert-to-vmstorage-packet-corrupt",
					Category:     "network",
					ChaosType:    "networkchaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmselect to vmstorage packet delay",
				Label("id=e13108bd-00df-40f5-acc9-b134bc619dc8"),
				ChaosScenario{
					ScenarioName: "vmselect-to-vmstorage-packet-delay",
					Category:     "network",
					ChaosType:    "networkchaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmstorage from vminsert packet loss",
				Label("id=490c384c-a995-4b46-a5c2-c37baa72beaf"),
				ChaosScenario{
					ScenarioName: "vmstorage-from-vminsert-packet-loss",
					Category:     "network",
					ChaosType:    "networkchaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmstorage from vmselect packet delay",
				Label("id=260857d8-c49e-4ac3-92e4-220addcc4a53"),
				ChaosScenario{
					ScenarioName: "vmstorage-from-vmselect-packet-delay",
					Category:     "network",
					ChaosType:    "networkchaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
		)

		DescribeTable("should handle HTTP chaos scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert request delay",
				Label("id=98f0368b-b200-4558-a09f-37e7ceaa3b1d"),
				ChaosScenario{
					ScenarioName: "vminsert-request-delay",
					Category:     "http",
					ChaosType:    "httpchaos",
					// CheckAlerts:  []string{"CustomTooHighSlowInsertsRate"},
				},
			),
			Entry("vminsert response abort",
				Label("id=d738fdd5-0076-4ddf-9358-2812a9cc3e2b"),
				ChaosScenario{
					ScenarioName: "vminsert-response-abort",
					Category:     "http",
					ChaosType:    "httpchaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmselect request delay",
				Label("id=3e1eff4c-dcda-442b-a477-85359ffc57b7"),
				ChaosScenario{
					ScenarioName: "vmselect-request-delay",
					Category:     "http",
					ChaosType:    "httpchaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmselect response abort",
				Label("id=b2807243-8528-4500-b630-822ed9fce73d"),
				ChaosScenario{
					ScenarioName: "vmselect-response-abort",
					Category:     "http",
					ChaosType:    "httpchaos",
					// CheckAlerts:  []string{"ServiceDown"},
				},
			),
		)
	})
})
