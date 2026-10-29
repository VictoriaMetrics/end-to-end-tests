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
	corev1 "k8s.io/api/core/v1"

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

var _ = Describe("VM chaos tests", Label("vm-chaos"), func() {

	// ChaosScenario represents a chaos test scenario configuration
	type ChaosScenario struct {
		ScenarioName string
		Category     string
		ChaosType    string
		StorageTGPS  int64
		CheckAlerts  []string
		// AlertExceptions lists alerts tolerated after the scenario completes but
		// not required to fire during it (e.g. incidental OOM-kill side effects).
		AlertExceptions []string
	}

	// Chaos Mesh resource kind follows from the category unless a scenario overrides it
	// (multi-step scenarios run as workflows).
	chaosKinds := map[string]string{
		"pods": "podchaos", "cpu": "stresschaos", "memory": "stresschaos",
		"io": "stresschaos", "network": "networkchaos", "http": "httpchaos",
	}

	chaosEntry := func(name, id string, scenario ChaosScenario) TableEntry {
		return Entry(name, Label("id="+id), FlakeAttempts(2), SpecTimeout(consts.ChaosSpecTimeout), scenario)
	}

	// Helper function to run a chaos scenario
	runChaosScenario := func(ctx context.Context, scenario ChaosScenario) {
		testStart := time.Now()
		resourceIdentifier := tests.ResourceIdentifier(scenario.ScenarioName)
		namespace := tests.RandomNamespace(fmt.Sprintf("vm-%s", resourceIdentifier))
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)

		DeferCleanup(func(ctx context.Context) {
			tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
			install.DeleteVMCluster(t, kubeOpts, namespace)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		}, NodeTimeout(consts.GatherCleanupTimeout))

		By("Preparing chaos namespace")
		tests.PrepareChaosNamespace(ctx, t, namespace, kubeOpts, "vm-chaos-test=true")

		By("Checking no alerts are firing before chaos")
		overwatch.CheckNoAlertsFiring(ctx, t, namespace, promquery.DefaultExceptions)

		// Create new VMCluster object
		vmclient := install.GetVMClient(t, kubeOpts)

		clusterName := tests.ClusterName(fmt.Sprintf("vm-%s", resourceIdentifier))
		affinity := tests.VMClusterAffinity(clusterName, "vm-chaos-test")

		patches := tests.ClusterAffinityPatches(clusterName, affinity, []string{"vminsert", "vmselect", "vmstorage"})
		if scenario.StorageTGPS > 0 {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/vmstorage/terminationGracePeriodSeconds", scenario.StorageTGPS).
				MustBuild())
		}

		// By() before each risky call (not after) so a failure inside it is
		// attributed to this step instead of surfacing as an unattributed
		// failure on an earlier attempt (invisible in the Allure step list).
		By("Installing VMCluster")
		install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, patches, consts.PollingTimeout)

		// Ensure VMAgent remote write URL is set up
		remoteWriteURL := fmt.Sprintf(
			"http://vminsert-%s.%s.svc.cluster.local.:8480/insert/0/prometheus/api/v1/write",
			clusterName, namespace)
		logger.Default.Logf(t, "Setting vmagent remote write URL to %s", remoteWriteURL)
		By("Setting VMAgent remote write URL")
		install.EnsureVMAgentRemoteWriteURL(ctx, t, vmclient, kubeOpts, consts.DefaultVMNamespace, consts.DefaultReleaseName, remoteWriteURL)

		var storagePods []*corev1.Pod
		if scenario.ScenarioName == "vmstorage-ungraceful-restarts" {
			for i := range 2 {
				name := fmt.Sprintf("vmstorage-%s-%d", clusterName, i)
				storagePods = append(storagePods, k8s.GetPodContext(t, ctx, kubeOpts, name))
			}
		}

		chaosType := scenario.ChaosType
		if chaosType == "" {
			chaosType = chaosKinds[scenario.Category]
		}

		By(fmt.Sprintf("Running %s scenario", scenario.ScenarioName))
		dynamicClient := helpers.GetDynamicClient(t, kubeOpts)
		install.ApplyChaosScenario(ctx, t, namespace, clusterName, scenario.Category, scenario.ScenarioName)

		if len(scenario.CheckAlerts) > 0 {
			for _, alert := range scenario.CheckAlerts {
				By(fmt.Sprintf("Waiting for alert %s to fire", alert))
				overwatch.WaitUntilAlertFiring(ctx, t, namespace, alert)
			}
		}

		By("Waiting for chaos scenario to complete")
		install.WaitForChaosScenarioToComplete(ctx, t, dynamicClient, namespace, scenario.ScenarioName, chaosType)

		if len(storagePods) > 0 {
			By("Checking storage pods restarted")
		}
		for _, before := range storagePods {
			after := k8s.GetPodContext(t, ctx, kubeOpts, before.Name)
			Expect(install.PodRestarted(before, after)).To(BeTrue(), "chaos did not restart %s", before.Name)
		}

		By("No alerts are firing after chaos")
		exceptions := append(scenario.CheckAlerts, scenario.AlertExceptions...)
		overwatch.CheckNoAlertsFiring(ctx, t, namespace, exceptions)
	}

	Describe("pod restarts", Label("kind", "chaos-pod-failure"), func() {
		DescribeTable("should handle pod failure scenarios",
			runChaosScenario,
			chaosEntry("vminsert pod failure", "17f2e31b-9249-4283-845b-aae0bc81e5f2", ChaosScenario{
				ScenarioName: "vminsert-pod-failure",
				Category:     "pods",
				CheckAlerts:  []string{"ServiceDown"},
			}),
			chaosEntry("vmstorage pod failure", "e340d25f-b14f-4f21-acb4-68c4fdf39a85", ChaosScenario{
				ScenarioName: "vmstorage-pod-failure",
				Category:     "pods",
				CheckAlerts:  []string{"ServiceDown"},
			}),
			chaosEntry("vmstorage ungraceful restarts", "3d6fcc06-8452-49c6-9752-13303cf1d421", ChaosScenario{
				ScenarioName:    "vmstorage-ungraceful-restarts",
				Category:        "pods",
				ChaosType:       "workflows",
				StorageTGPS:     1,
				AlertExceptions: []string{"UncleanShutdown"},
			}),
			chaosEntry("vmselect pod failure", "38df1d4b-d38c-4064-8538-c0e03920255f", ChaosScenario{
				ScenarioName: "vmselect-pod-failure",
				Category:     "pods",
				CheckAlerts:  []string{"ServiceDown"},
			}),
		)
	})

	Describe("cpu stress", Label("kind", "chaos-cpu-stress"), func() {
		DescribeTable("should handle CPU stress scenarios",
			runChaosScenario,
			chaosEntry("vminsert CPU stress", "4c571bca-2442-4a1b-8e54-4f9878f8dd6d", ChaosScenario{
				ScenarioName: "vminsert-cpu-usage",
				Category:     "cpu",
				CheckAlerts:  []string{"CustomHighCPUUsage"},
			}),
			chaosEntry("vmstorage CPU stress", "d1ebdfd3-a0cf-4525-89b9-e998ec7b0c1e", ChaosScenario{
				ScenarioName: "vmstorage-cpu-usage",
				Category:     "cpu",
				CheckAlerts:  []string{"CustomHighCPUUsage"},
			}),
			chaosEntry("vmselect CPU stress", "f6637d83-be2a-44ab-b446-9c755bad4292", ChaosScenario{
				ScenarioName: "vmselect-cpu-usage",
				Category:     "cpu",
				CheckAlerts:  []string{"CustomHighCPUUsage"},
			}),
		)
	})

	Describe("memory stress", Label("kind", "chaos-memory-stress"), func() {
		DescribeTable("should handle memory stress scenarios",
			runChaosScenario,
			chaosEntry("vminsert memory stress", "47690837-45e5-4cae-9e60-abadf59e4e66", ChaosScenario{
				ScenarioName: "vminsert-memory-usage",
				Category:     "memory",
				CheckAlerts:  []string{"CustomHighMemoryUsage"},
				// Component may be OOM-killed under this stress level, causing
				// an incidental ungraceful restart.
				AlertExceptions: []string{"UncleanShutdown"},
			}),
			chaosEntry("vmstorage memory stress", "357cef7e-c2ce-4a76-8768-7b142a4e7997", ChaosScenario{
				ScenarioName: "vmstorage-memory-usage",
				Category:     "memory",
				CheckAlerts:  []string{"CustomHighMemoryUsage"},
				// Component may be OOM-killed under this stress level, causing
				// an incidental ungraceful restart.
				AlertExceptions: []string{"UncleanShutdown"},
			}),
			chaosEntry("vmselect memory stress", "f9c922b8-104a-4baf-bad3-b00188ccddb1", ChaosScenario{
				ScenarioName: "vmselect-memory-usage",
				Category:     "memory",
				CheckAlerts:  []string{"CustomHighMemoryUsage"},
				// Component may be OOM-killed under this stress level, causing
				// an incidental ungraceful restart.
				AlertExceptions: []string{"UncleanShutdown"},
			}),
		)
	})

	Describe("io stress", Label("kind", "chaos-io-stress"), func() {
		DescribeTable("should handle IO stress scenarios",
			runChaosScenario,
			chaosEntry("vminsert IO stress", "c70ce6cc-84fe-447d-8b5f-48871a2ebf99", ChaosScenario{
				ScenarioName: "vminsert-io-usage",
				Category:     "io",
			}),
			chaosEntry("vmstorage IO stress", "8b3f1e4a-2c5d-4f67-9aab-123456abcdef", ChaosScenario{
				ScenarioName: "vmstorage-io-usage",
				Category:     "io",
			}),
			chaosEntry("vmselect IO stress", "9c4d2b3a-1f0e-4d6c-8b7a-abcdef123456", ChaosScenario{
				ScenarioName: "vmselect-io-usage",
				Category:     "io",
			}),
		)
	})

	Describe("network failure", Label("kind", "chaos-network-failure"), func() {
		DescribeTable("should handle network chaos scenarios",
			runChaosScenario,
			chaosEntry("vminsert to vmstorage packet corrupt", "ef3455cd-7687-49a4-b423-7c4541aa051c", ChaosScenario{
				ScenarioName: "vminsert-to-vmstorage-packet-corrupt",
				Category:     "network",
				CheckAlerts:  []string{"CustomRPCErrors"},
			}),
			chaosEntry("vmselect to vmstorage packet delay", "e13108bd-00df-40f5-acc9-b134bc619dc8", ChaosScenario{
				ScenarioName: "vmselect-to-vmstorage-packet-delay",
				Category:     "network",
			}),
			chaosEntry("vmstorage from vminsert packet loss", "490c384c-a995-4b46-a5c2-c37baa72beaf", ChaosScenario{
				ScenarioName: "vmstorage-from-vminsert-packet-loss",
				Category:     "network",
				CheckAlerts:  []string{"CustomRPCErrors"},
			}),
			chaosEntry("vmstorage from vmselect packet delay", "260857d8-c49e-4ac3-92e4-220addcc4a53", ChaosScenario{
				ScenarioName: "vmstorage-from-vmselect-packet-delay",
				Category:     "network",
				CheckAlerts:  []string{"CustomRPCErrors"},
			}),
		)

		DescribeTable("should handle HTTP chaos scenarios",
			runChaosScenario,
			chaosEntry("vminsert request delay", "98f0368b-b200-4558-a09f-37e7ceaa3b1d", ChaosScenario{
				ScenarioName: "vminsert-request-delay",
				Category:     "http",
			}),
			chaosEntry("vminsert response abort", "d738fdd5-0076-4ddf-9358-2812a9cc3e2b", ChaosScenario{
				ScenarioName: "vminsert-response-abort",
				Category:     "http",
			}),
			chaosEntry("vmselect request delay", "3e1eff4c-dcda-442b-a477-85359ffc57b7", ChaosScenario{
				ScenarioName: "vmselect-request-delay",
				Category:     "http",
			}),
			chaosEntry("vmselect response abort", "b2807243-8528-4500-b630-822ed9fce73d", ChaosScenario{
				ScenarioName: "vmselect-response-abort",
				Category:     "http",
			}),
		)
	})
})
