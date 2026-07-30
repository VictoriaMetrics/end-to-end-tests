package vl_chaos_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
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

		// Stage 1 (parallel): discover ingress host + install chaos mesh.
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

		// Stage 2 (parallel): vmgather + vm k8s stack + victorialogs single (log storage used
		// by GatherOnFailure to pull pod logs on a failed spec).
		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMGather(ctx, t)
		}()
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			install.InstallVMK8StackWithHelm(ctx, consts.VMK8sStackChart, consts.SmokeValuesFile(), t, consts.DefaultVMNamespace, consts.DefaultReleaseName)
			install.InstallVictoriaLogs(ctx, t, consts.DefaultVMNamespace, consts.DefaultVLReleaseName, consts.DefaultVLCollectorReleaseName)
		}()
		wg.Wait()

		// Stage 3: clean up any stale chaos-test namespaces left over from a previous run, then
		// install overwatch.
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "delete", "namespace", "-l", "vl-chaos-test=true",
			"--ignore-not-found=true", "--wait=true", fmt.Sprintf("--timeout=%s", consts.PollingTimeout))
		install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)
	}, func(ctx context.Context) {
		t = tests.GetT()
	},
)

var _ = Describe("Chaos tests", Label("chaos-test"), func() {

	// ChaosScenario represents a chaos test scenario configuration.
	type ChaosScenario struct {
		ScenarioName string
		Category     string
		ChaosType    string
	}

	// Helper function to run a chaos scenario against a fresh, isolated VLCluster.
	runChaosScenario := func(ctx context.Context, scenario ChaosScenario) {
		namespace := tests.RandomNamespace(fmt.Sprintf("vl-%s", scenario.ScenarioName))
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		clusterName := tests.ClusterName(fmt.Sprintf("vl-%s", scenario.ScenarioName))

		DeferCleanup(func(ctx context.Context) {
			tests.GatherOnFailure(ctx, t, kubeOpts, namespace)
			install.DeleteVLCluster(t, kubeOpts, clusterName)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		tests.CleanupNamespace(t, kubeOpts, namespace)
		tests.EnsureNamespaceExists(t, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "vl-chaos-test=true", "--overwrite")

		affinity := tests.VLClusterAffinity(clusterName, "vl-chaos-test")
		install.InstallVLCluster(ctx, t, kubeOpts, namespace, clusterName, affinity)
		By("VLCluster is available")

		By(fmt.Sprintf("Running %s scenario", scenario.ScenarioName))
		dynamicClient := install.GetDynamicClient(t, kubeOpts)
		install.ApplyChaosScenario(ctx, t, namespace, scenario.Category, scenario.ScenarioName)

		By("Waiting for chaos scenario to complete")
		install.WaitForChaosScenarioToComplete(ctx, t, dynamicClient, namespace, scenario.ScenarioName, scenario.ChaosType)
	}

	Describe("pod restarts", Label("kind", "chaos-pod-failure"), func() {
		DescribeTable("should handle pod failure scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vlinsert pod failure",
				Label("id=722e6fa7-d5da-4f7b-a2a0-7a813f5a7735"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlinsert-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
				},
			),
			Entry("vlselect pod failure",
				Label("id=d09d484b-28ff-47d2-b601-b63eefd0e2b0"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlselect-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
				},
			),
			Entry("vlstorage pod failure",
				Label("id=dc41f40e-7fd9-4834-a3b9-5bc0fd1338a0"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlstorage-pod-failure",
					Category:     "pods",
					ChaosType:    "podchaos",
				},
			),
		)
	})

	Describe("cpu stress", Label("kind", "chaos-cpu-stress"), func() {
		DescribeTable("should handle CPU stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vlinsert CPU stress",
				Label("id=794f61a8-5596-4152-b637-3aac9b185f1a"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlinsert-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vlselect CPU stress",
				Label("id=ec96f9ae-06e1-4ff1-a29f-9533146eafbf"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlselect-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vlstorage CPU stress",
				Label("id=a5580164-5573-414a-a0ce-bb332190069f"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlstorage-cpu-usage",
					Category:     "cpu",
					ChaosType:    "stresschaos",
				},
			),
		)
	})

	Describe("memory stress", Label("kind", "chaos-memory-stress"), func() {
		DescribeTable("should handle memory stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vlinsert memory stress",
				Label("id=ef3abbdb-b8e8-405d-82ca-1b15b7ea5a57"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlinsert-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vlselect memory stress",
				Label("id=b334c2e0-4228-48b9-9c46-45ad25737641"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlselect-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vlstorage memory stress",
				Label("id=9126e59e-4272-4165-a204-505e7425a192"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlstorage-memory-usage",
					Category:     "memory",
					ChaosType:    "stresschaos",
				},
			),
		)
	})

	Describe("io stress", Label("kind", "chaos-io-stress"), func() {
		DescribeTable("should handle IO stress scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vlinsert IO stress",
				Label("id=2e712ee2-b74a-43f7-bb5c-73495a877fc0"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlinsert-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vlstorage IO stress",
				Label("id=4ba919fb-0671-4aec-b7b2-2246e3a47515"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlstorage-io-usage",
					Category:     "io",
					ChaosType:    "stresschaos",
				},
			),
			Entry("vlselect IO stress",
				Label("id=ac550f27-785e-45dc-8e1b-7b3f4e0d1734"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlselect-io-usage",
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
			Entry("vlinsert to vlstorage packet corrupt",
				Label("id=75f040fd-3eb7-4be5-b011-9df4648a8c05"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlinsert-to-vlstorage-packet-corrupt",
					Category:     "network",
					ChaosType:    "networkchaos",
				},
			),
			Entry("vlselect to vlstorage packet delay",
				Label("id=1640795b-c1ac-45c5-8cc3-b499b634347f"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlselect-to-vlstorage-packet-delay",
					Category:     "network",
					ChaosType:    "networkchaos",
				},
			),
			Entry("vlstorage from vlinsert packet loss",
				Label("id=1af3dcf6-f352-43c4-a9d8-25e1108bad86"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlstorage-from-vlinsert-packet-loss",
					Category:     "network",
					ChaosType:    "networkchaos",
				},
			),
			Entry("vlstorage from vlselect packet delay",
				Label("id=ee007b4b-23f6-42ca-998a-66312b96029b"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlstorage-from-vlselect-packet-delay",
					Category:     "network",
					ChaosType:    "networkchaos",
				},
			),
		)

		DescribeTable("should handle HTTP chaos scenarios",
			func(ctx context.Context, scenario ChaosScenario) {
				runChaosScenario(ctx, scenario)
			},
			Entry("vlinsert request delay",
				Label("id=f00ad46a-f41a-4129-ac1a-8c0b87d9f7cc"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlinsert-request-delay",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
			Entry("vlinsert response abort",
				Label("id=77a8b321-c4a9-4b39-ae96-0a8d31cc8843"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlinsert-response-abort",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
			Entry("vlselect request delay",
				Label("id=401ffdca-1eff-459f-abb3-16e106d77f81"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlselect-request-delay",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
			Entry("vlselect response abort",
				Label("id=72ea3998-1782-4118-80bf-3518a9430dfe"),
				FlakeAttempts(2),
				SpecTimeout(consts.ChaosSpecTimeout),
				ChaosScenario{
					ScenarioName: "vlselect-response-abort",
					Category:     "http",
					ChaosType:    "httpchaos",
				},
			),
		)
	})
})
