package chaos_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestChaosTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Chaos test Suite", suiteConfig, reporterConfig)
}

var (
	t         terratesting.TestingT
	overwatch promquery.PrometheusClient
)

// Install VM from helm chart for the first process, set namespace for the rest
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()
		chaosCfg := tests.DefaultChaosMeshConfig()

		// Stage 1: install VPA + Gateway API CRDs before the operator starts. Doing this
		// first (not after InstallVMStackAndGather) means the operator's own RESTMapper
		// discovers these Kinds at boot instead of racing a CRD applied after it is already
		// running - that race made the operator hard-fail reconciles with
		// `no matches for kind "VerticalPodAutoscaler"` until its cache eventually refreshed.
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.EnsureVPACRDs(ctx, t, kubeOpts)
		install.EnsureGatewayAPICRDs(ctx, t, kubeOpts)

		// Stage 2 (parallel): discover ingress host + install chaos mesh.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.DiscoverIngressHost(ctx, t)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallChaosMesh(ctx, chaosCfg.HelmChart, chaosCfg.ValuesFile, t, chaosCfg.Namespace, chaosCfg.ReleaseName)
		}()
		wg.Wait()

		// Stage 3: install vmgather + vm k8s stack (both need ingress host).
		tests.InstallVMStackAndGather(ctx, t)

		// Stage 4: overwatch + delete stock vmcluster + alert rules.
		tests.CleanupStaleNamespaces(ctx, t, kubeOpts, "vm-chaos-test=true")
		tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{DeleteVMCluster: true, AddCustomAlertRules: true})
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
		// AlertExceptions lists alerts tolerated after the scenario completes but
		// not required to fire during it (e.g. incidental OOM-kill side effects).
		AlertExceptions []string
	}

	// Helper function to run a chaos scenario
	runChaosScenario := func(ctx context.Context, scenario ChaosScenario) {
		testStart := time.Now()
		namespace := tests.RandomNamespace(fmt.Sprintf("vm-%s", scenario.ScenarioName))
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)

		DeferCleanup(func(ctx context.Context) {
			tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
			install.DeleteVMCluster(t, kubeOpts, namespace)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		}, NodeTimeout(consts.GatherCleanupTimeout))

		tests.PrepareChaosNamespace(ctx, t, namespace, kubeOpts, "vm-chaos-test=true")

		overwatch.CheckNoAlertsFiring(ctx, t, namespace, promquery.DefaultExceptions)

		// Create new VMCluster object
		vmclient := install.GetVMClient(t, kubeOpts)

		clusterName := tests.ClusterName(fmt.Sprintf("vm-%s", scenario.ScenarioName))
		affinity := tests.VMClusterAffinity(clusterName, "vm-chaos-test")

		patches := tests.ClusterAffinityPatches(clusterName, affinity, []string{"vminsert", "vmselect", "vmstorage"})

		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, patches, consts.PollingTimeout)
		By("VMCluster is available")

		// Ensure VMAgent remote write URL is set up
		remoteWriteURL := fmt.Sprintf(
			"http://vminsert-%s.%s.svc.cluster.local.:8480/insert/0/prometheus/api/v1/write",
			clusterName, namespace)
		logger.Default.Logf(t, "Setting vmagent remote write URL to %s", remoteWriteURL)
		install.EnsureVMAgentRemoteWriteURL(ctx, t, vmclient, kubeOpts, consts.DefaultVMNamespace, consts.DefaultReleaseName, remoteWriteURL)

		By(fmt.Sprintf("Running %s scenario", scenario.ScenarioName))
		dynamicClient := helpers.GetDynamicClient(t, kubeOpts)
		install.ApplyChaosScenario(ctx, t, namespace, scenario.Category, scenario.ScenarioName)

		if len(scenario.CheckAlerts) > 0 {
			for _, alert := range scenario.CheckAlerts {
				By(fmt.Sprintf("Waiting for alert %s to fire", alert))
				overwatch.WaitUntilAlertFiring(ctx, t, namespace, alert)
			}
		}

		By("Waiting for chaos scenario to complete")
		install.WaitForChaosScenarioToComplete(ctx, t, dynamicClient, namespace, scenario.ScenarioName, scenario.ChaosType)

		By("No alerts are firing after chaos")
		exceptions := append(scenario.CheckAlerts, scenario.AlertExceptions...)
		overwatch.CheckNoAlertsFiring(ctx, t, namespace, exceptions)
	}

	Describe("pod restarts", Label("kind", "chaos-pod-failure"), func() {
		DescribeTable("should handle pod failure scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert pod failure",
				Label("id=17f2e31b-9249-4283-845b-aae0bc81e5f2"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vminsert-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
					CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmstorage pod failure",
				Label("id=e340d25f-b14f-4f21-acb4-68c4fdf39a85"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmstorage-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
					CheckAlerts:  []string{"ServiceDown"},
				},
			),
			Entry("vmselect pod failure",
				Label("id=38df1d4b-d38c-4064-8538-c0e03920255f"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmselect-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
					CheckAlerts:  []string{"ServiceDown"},
				},
			),
		)
	})

	Describe("cpu stress", Label("kind", "chaos-cpu-stress"), func() {
		DescribeTable("should handle CPU stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert CPU stress",
				Label("id=4c571bca-2442-4a1b-8e54-4f9878f8dd6d"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vminsert-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomHighCPUUsage"},
				},
			),
			Entry("vmstorage CPU stress",
				Label("id=d1ebdfd3-a0cf-4525-89b9-e998ec7b0c1e"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmstorage-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomHighCPUUsage"},
				},
			),
			Entry("vmselect CPU stress",
				Label("id=f6637d83-be2a-44ab-b446-9c755bad4292"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmselect-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomHighCPUUsage"},
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
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vminsert-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomHighMemoryUsage"},
					// Component may be OOM-killed under this stress level, causing
					// an incidental ungraceful restart.
					AlertExceptions: []string{"UncleanShutdown"},
				},
			),
			Entry("vmstorage memory stress",
				Label("id=357cef7e-c2ce-4a76-8768-7b142a4e7997"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmstorage-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomHighMemoryUsage"},
					// Component may be OOM-killed under this stress level, causing
					// an incidental ungraceful restart.
					AlertExceptions: []string{"UncleanShutdown"},
				},
			),
			Entry("vmselect memory stress",
				Label("id=f9c922b8-104a-4baf-bad3-b00188ccddb1"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmselect-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
					CheckAlerts:  []string{"CustomHighMemoryUsage"},
					// Component may be OOM-killed under this stress level, causing
					// an incidental ungraceful restart.
					AlertExceptions: []string{"UncleanShutdown"},
				},
			),
		)
	})

	Describe("io stress", Label("kind", "chaos-io-stress"), func() {
		DescribeTable("should handle IO stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert IO stress",
				Label("id=c70ce6cc-84fe-447d-8b5f-48871a2ebf99"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vminsert-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vmstorage IO stress",
				Label("id=8b3f1e4a-2c5d-4f67-9aab-123456abcdef"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmstorage-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vmselect IO stress",
				Label("id=9c4d2b3a-1f0e-4d6c-8b7a-abcdef123456"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					UUID:         "9c4d2b3a-1f0e-4d6c-8b7a-abcdef123456",
					ScenarioName: "vmselect-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
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
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vminsert-to-vmstorage-packet-corrupt",
					Category:     "network",
					ChaosType:    "networkchaos",
					CheckAlerts:  []string{"CustomRPCErrors"},
				},
			),
			Entry("vmselect to vmstorage packet delay",
				Label("id=e13108bd-00df-40f5-acc9-b134bc619dc8"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmselect-to-vmstorage-packet-delay",
					Category:     "network",
					ChaosType:    "networkchaos",
				},
			),
			Entry("vmstorage from vminsert packet loss",
				Label("id=490c384c-a995-4b46-a5c2-c37baa72beaf"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmstorage-from-vminsert-packet-loss",
					Category:     "network",
					ChaosType:    "networkchaos",
					CheckAlerts:  []string{"CustomRPCErrors"},
				},
			),
			Entry("vmstorage from vmselect packet delay",
				Label("id=260857d8-c49e-4ac3-92e4-220addcc4a53"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmstorage-from-vmselect-packet-delay",
					Category:     "network",
					ChaosType:    "networkchaos",
					CheckAlerts:  []string{"CustomRPCErrors"},
				},
			),
		)

		DescribeTable("should handle HTTP chaos scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vminsert request delay",
				Label("id=98f0368b-b200-4558-a09f-37e7ceaa3b1d"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vminsert-request-delay",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
			Entry("vminsert response abort",
				Label("id=d738fdd5-0076-4ddf-9358-2812a9cc3e2b"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vminsert-response-abort",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
			Entry("vmselect request delay",
				Label("id=3e1eff4c-dcda-442b-a477-85359ffc57b7"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmselect-request-delay",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
			Entry("vmselect response abort",
				Label("id=b2807243-8528-4500-b630-822ed9fce73d"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vmselect-response-abort",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
		)
	})
})
