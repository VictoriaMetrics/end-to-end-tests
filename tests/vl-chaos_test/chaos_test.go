package vl_chaos_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	corev1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestVLChaosTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "VL Chaos test Suite", suiteConfig, reporterConfig)
}

var (
	t terratesting.TestingT
)

// Install the shared management/observability stack for the first process, set namespace for the rest.
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

		// Stage 3 (parallel): vmgather + vm k8s stack + victorialogs single (log storage used
		// by GatherOnFailure to pull pod logs on a failed spec).
		tests.InstallVMStackAndGather(ctx, t)

		// Stage 4: clean up any stale chaos-test namespaces left over from a previous run, then
		// install overwatch.
		tests.CleanupStaleNamespaces(ctx, t, kubeOpts, "vl-chaos-test=true")
		tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})
	}, func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = Describe("VL chaos tests", Label("vl-chaos"), func() {

	// ChaosScenario represents a chaos test scenario configuration.
	type ChaosScenario struct {
		ScenarioName string
		Category     string
		ChaosType    string
		StorageTGPS  int64
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

	// Helper function to run a chaos scenario against a fresh, isolated VLCluster.
	runChaosScenario := func(ctx context.Context, scenario ChaosScenario) {
		testStart := time.Now()
		namespace := tests.RandomNamespace(fmt.Sprintf("vl-%s", scenario.ScenarioName))
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		clusterName := tests.ClusterName(fmt.Sprintf("vl-%s", scenario.ScenarioName))

		DeferCleanup(func(ctx context.Context) {
			tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
			install.DeleteVLCluster(t, kubeOpts, clusterName)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		}, NodeTimeout(consts.GatherCleanupTimeout))

		tests.PrepareChaosNamespace(ctx, t, namespace, kubeOpts, "vl-chaos-test=true")

		vlclient := install.GetVMClient(t, kubeOpts)

		affinity := tests.VLClusterAffinity(clusterName, "vl-chaos-test")
		patches := tests.ClusterAffinityPatches(clusterName, affinity, []string{"vlinsert", "vlselect", "vlstorage"})
		if scenario.StorageTGPS > 0 {
			patches = append(patches, tests.NewJSONPatchBuilder().
				Add("/spec/vlstorage/terminationGracePeriodSeconds", scenario.StorageTGPS).
				MustBuild())
		}

		install.InstallVLCluster(ctx, t, kubeOpts, namespace, vlclient, patches, consts.PollingTimeout)
		By("VLCluster is available")

		var storagePods []*corev1.Pod
		if scenario.ScenarioName == "vlstorage-ungraceful-restarts" {
			for i := range 2 {
				name := fmt.Sprintf("vlstorage-%s-%d", clusterName, i)
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

		By("Waiting for chaos scenario to complete")
		install.WaitForChaosScenarioToComplete(ctx, t, dynamicClient, namespace, scenario.ScenarioName, chaosType)

		if len(storagePods) > 0 {
			By("Checking storage pods restarted")
		}
		for _, before := range storagePods {
			after := k8s.GetPodContext(t, ctx, kubeOpts, before.Name)
			Expect(install.PodRestarted(before, after)).To(BeTrue(), "chaos did not restart %s", before.Name)
		}
	}

	Describe("pod restarts", Label("kind", "chaos-pod-failure"), func() {
		DescribeTable("should handle pod failure scenarios",
			runChaosScenario,
			chaosEntry("vlinsert pod failure", "722e6fa7-d5da-4f7b-a2a0-7a813f5a7735", ChaosScenario{
				ScenarioName: "vlinsert-pod-failure",
				Category:     "pods",
			}),
			chaosEntry("vlselect pod failure", "d09d484b-28ff-47d2-b601-b63eefd0e2b0", ChaosScenario{
				ScenarioName: "vlselect-pod-failure",
				Category:     "pods",
			}),
			chaosEntry("vlstorage pod failure", "dc41f40e-7fd9-4834-a3b9-5bc0fd1338a0", ChaosScenario{
				ScenarioName: "vlstorage-pod-failure",
				Category:     "pods",
			}),
			chaosEntry("vlstorage ungraceful restarts", "14523b7d-2b6a-4ad1-943a-b46e1e731841", ChaosScenario{
				ScenarioName: "vlstorage-ungraceful-restarts",
				Category:     "pods",
				ChaosType:    "workflows",
				StorageTGPS:  1,
			}),
		)
	})

	Describe("cpu stress", Label("kind", "chaos-cpu-stress"), func() {
		DescribeTable("should handle CPU stress scenarios",
			runChaosScenario,
			chaosEntry("vlinsert CPU stress", "794f61a8-5596-4152-b637-3aac9b185f1a", ChaosScenario{
				ScenarioName: "vlinsert-cpu-usage",
				Category:     "cpu",
			}),
			chaosEntry("vlselect CPU stress", "ec96f9ae-06e1-4ff1-a29f-9533146eafbf", ChaosScenario{
				ScenarioName: "vlselect-cpu-usage",
				Category:     "cpu",
			}),
			chaosEntry("vlstorage CPU stress", "a5580164-5573-414a-a0ce-bb332190069f", ChaosScenario{
				ScenarioName: "vlstorage-cpu-usage",
				Category:     "cpu",
			}),
		)
	})

	Describe("memory stress", Label("kind", "chaos-memory-stress"), func() {
		DescribeTable("should handle memory stress scenarios",
			runChaosScenario,
			chaosEntry("vlinsert memory stress", "ef3abbdb-b8e8-405d-82ca-1b15b7ea5a57", ChaosScenario{
				ScenarioName: "vlinsert-memory-usage",
				Category:     "memory",
			}),
			chaosEntry("vlselect memory stress", "b334c2e0-4228-48b9-9c46-45ad25737641", ChaosScenario{
				ScenarioName: "vlselect-memory-usage",
				Category:     "memory",
			}),
			chaosEntry("vlstorage memory stress", "9126e59e-4272-4165-a204-505e7425a192", ChaosScenario{
				ScenarioName: "vlstorage-memory-usage",
				Category:     "memory",
			}),
		)
	})

	Describe("io stress", Label("kind", "chaos-io-stress"), func() {
		DescribeTable("should handle IO stress scenarios",
			runChaosScenario,
			chaosEntry("vlinsert IO stress", "2e712ee2-b74a-43f7-bb5c-73495a877fc0", ChaosScenario{
				ScenarioName: "vlinsert-io-usage",
				Category:     "io",
			}),
			chaosEntry("vlstorage IO stress", "4ba919fb-0671-4aec-b7b2-2246e3a47515", ChaosScenario{
				ScenarioName: "vlstorage-io-usage",
				Category:     "io",
			}),
			chaosEntry("vlselect IO stress", "ac550f27-785e-45dc-8e1b-7b3f4e0d1734", ChaosScenario{
				ScenarioName: "vlselect-io-usage",
				Category:     "io",
			}),
		)
	})

	Describe("network failure", Label("kind", "chaos-network-failure"), func() {
		DescribeTable("should handle network chaos scenarios",
			runChaosScenario,
			chaosEntry("vlinsert to vlstorage packet corrupt", "75f040fd-3eb7-4be5-b011-9df4648a8c05", ChaosScenario{
				ScenarioName: "vlinsert-to-vlstorage-packet-corrupt",
				Category:     "network",
			}),
			chaosEntry("vlselect to vlstorage packet delay", "1640795b-c1ac-45c5-8cc3-b499b634347f", ChaosScenario{
				ScenarioName: "vlselect-to-vlstorage-packet-delay",
				Category:     "network",
			}),
			chaosEntry("vlstorage from vlinsert packet loss", "1af3dcf6-f352-43c4-a9d8-25e1108bad86", ChaosScenario{
				ScenarioName: "vlstorage-from-vlinsert-packet-loss",
				Category:     "network",
			}),
			chaosEntry("vlstorage from vlselect packet delay", "ee007b4b-23f6-42ca-998a-66312b96029b", ChaosScenario{
				ScenarioName: "vlstorage-from-vlselect-packet-delay",
				Category:     "network",
			}),
		)

		DescribeTable("should handle HTTP chaos scenarios",
			runChaosScenario,
			chaosEntry("vlinsert request delay", "f00ad46a-f41a-4129-ac1a-8c0b87d9f7cc", ChaosScenario{
				ScenarioName: "vlinsert-request-delay",
				Category:     "http",
			}),
			chaosEntry("vlinsert response abort", "77a8b321-c4a9-4b39-ae96-0a8d31cc8843", ChaosScenario{
				ScenarioName: "vlinsert-response-abort",
				Category:     "http",
			}),
			chaosEntry("vlselect request delay", "401ffdca-1eff-459f-abb3-16e106d77f81", ChaosScenario{
				ScenarioName: "vlselect-request-delay",
				Category:     "http",
			}),
			chaosEntry("vlselect response abort", "72ea3998-1782-4118-80bf-3518a9430dfe", ChaosScenario{
				ScenarioName: "vlselect-response-abort",
				Category:     "http",
			}),
		)
	})
})
