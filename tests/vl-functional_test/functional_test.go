package vl_functional_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

// mTLS is VictoriaLogs' only Enterprise feature: it requires both the
// "-enterprise" image tag and a valid license, checked via consts.LicenseFile().
const (
	vlMTLSSecretName    = "vl-mtls"
	vlMTLSClientPodName = "vl-mtls-client"
	vmAuthConfigSecret  = "vmauth-config"
)

func TestVLFunctionalTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.FlakeAttempts = 3
	RunSpecs(t, "VictoriaLogs Functional test Suite", suiteConfig, reporterConfig)
}

var (
	t         terratesting.TestingT
	namespace string
)

// Install VictoriaLogs stack for the first process, propagate t to the rest.
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()

		// Stage 1: install VPA + Gateway API CRDs before the operator starts. Doing this
		// first (not after InstallVMStackAndGather) means the operator's own RESTMapper
		// discovers these Kinds at boot instead of racing a CRD applied after it is already
		// running - that race made the operator hard-fail reconciles with
		// `no matches for kind "VerticalPodAutoscaler"` until its cache eventually refreshed.
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.EnsureVPACRDs(ctx, t, kubeOpts)
		install.EnsureGatewayAPICRDs(ctx, t, kubeOpts)

		// Clean up stale namespaces left by previous killed enterprise runs.
		if consts.LicenseFile() != "" {
			tests.CleanupStaleNamespaces(ctx, t, kubeOpts, "vl-enterprise-test=true")
		}

		install.DiscoverIngressHost(ctx, t)

		// Stage 2 (parallel): vmgather + vm k8s stack + victorialogs single + collector (all need ingress host).
		tests.InstallVMStackAndGather(ctx, t)

		// Stage 3: install overwatch. Needs the VMAgent/VMAlert/VMSingle CRDs and the
		// "vmks" CRs installed by InstallVMK8StackWithHelm above.
		tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

// installVLSingle installs victoria-logs-single in the given namespace and returns its ingress URL.
func installVLSingle(ctx context.Context, ns, releaseName string) string {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLSingleChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}
	opts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.VictoriaLogsSingleValuesFile()},
		SetValues: map[string]string{
			"server.ingress.enabled":          "true",
			"server.ingress.ingressClassName": "traefik",
			"server.ingress.hosts[0].name":    consts.VLNamespacedHost(ns),
			"server.ingress.hosts[0].path[0]": "/",
		},
		ExtraArgs: map[string][]string{"upgrade": upgradeArgs},
	}
	if v := consts.VLVersion(); v != "" {
		opts.SetValues["server.image.tag"] = v
	}
	By(fmt.Sprintf("Install %s as %s in %s", consts.VictoriaLogsSingleChart, releaseName, ns))
	if err := helm.UpgradeE(t, opts, consts.VictoriaLogsSingleChart, releaseName); err != nil {
		t.Fatalf("failed to install %s: %v", consts.VictoriaLogsSingleChart, err)
	}
	vlURL := consts.VLUrl(ns)
	helpers.WaitForHTTPRoute(ctx, t, vlURL+"/health")
	return vlURL
}

// uninstallRelease removes a Helm release from the namespace.
func uninstallRelease(ns, releaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	opts := &helm.Options{KubectlOptions: kubeOpts}
	By(fmt.Sprintf("Uninstall %s from %s", releaseName, ns))
	_ = helm.DeleteE(t, opts, releaseName, true)
}

// installVLCollector installs victoria-logs-collector pointed at the given VLSingle insert URL.
func installVLCollector(ctx context.Context, ns, releaseName, vlSingleAddr string) {
	kubeOpts := k8s.NewKubectlOptions("", "", ns)
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLCollectorChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}
	opts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.VictoriaLogsCollectorValuesFile()},
		SetValues:      map[string]string{"remoteWrite[0].url": fmt.Sprintf("http://%s", vlSingleAddr)},
		ExtraArgs:      map[string][]string{"upgrade": upgradeArgs},
	}
	By(fmt.Sprintf("Install %s as %s in %s", consts.VictoriaLogsCollectorChart, releaseName, ns))
	if err := helm.UpgradeE(t, opts, consts.VictoriaLogsCollectorChart, releaseName); err != nil {
		t.Fatalf("failed to install %s: %v", consts.VictoriaLogsCollectorChart, err)
	}
}

var _ = Describe("VLSingle", Label("vlsingle"), func() {
	const releaseName = "vl-single-test"
	var vlURL string
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		namespace = tests.RandomNamespace("vl")
		vlURL = installVLSingle(ctx, namespace, releaseName)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		uninstallRelease(namespace, releaseName)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should ingest and query logs back",
		Label("id=e40691aa-e79e-41d1-9397-7585aaac4f9a"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"hello vlsingle","test_id":%q}`,
				ingestTime.Format(time.RFC3339Nano), testLabel)

			Expect(tests.VLIngest(ctx, vlURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := tests.VLQuery(ctx, vlURL, fmt.Sprintf(`{test_id=%q}`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello vlsingle"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=338d24c1-7639-42e6-b9a1-9d99d6dac6d3"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)
			now := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"stats test","test_id":%q}`,
				now.Format(time.RFC3339Nano), testLabel)

			Expect(tests.VLIngest(ctx, vlURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := tests.VLStatsCount(ctx, vlURL, `* | stats count() total`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("total"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return 200 for a wildcard query",
		Label("id=f3a6ea21-081e-4f72-84c8-80fbca451cf0"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			Eventually(func(g Gomega) {
				_, err := tests.VLQuery(ctx, vlURL, "*",
					time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should ingest Elasticsearch bulk logs",
		Label("id=6d4f6357-92d6-4db5-a42c-f8f3c02f8668"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-es-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"create":{}}
{"_time":%q,"_msg":"hello elasticsearch bulk","test_id":%q}
`, ingestTime.Format(time.RFC3339Nano), testLabel)

			Expect(tests.VLPost(ctx, vlURL+"/insert/elasticsearch/_bulk", "application/json", []byte(payload), "elasticsearch bulk ingest")).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := tests.VLQuery(ctx, vlURL, fmt.Sprintf(`test_id:%q`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello elasticsearch bulk"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should ingest Loki push logs",
		Label("id=cfdd5ac7-7080-4974-8fdd-0f6e6f6a8263"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-loki-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"streams":[{"stream":{"test_id":%q,"job":"e2e"},"values":[[%q,"hello loki push"]]}]}`,
				testLabel, fmt.Sprintf("%d", ingestTime.UnixNano()))

			Expect(tests.VLPost(ctx, vlURL+"/insert/loki/api/v1/push", "application/json", []byte(payload), "loki push ingest")).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := tests.VLQuery(ctx, vlURL, fmt.Sprintf(`{test_id=%q} "hello loki push"`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello loki push"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should ingest OTLP logs",
		Label("id=f1692c65-93b0-420d-88b9-952b38c35012"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-otlp-%s", namespace)
			ingestTime := time.Now().UTC()

			payload, err := proto.Marshal(&collogspb.ExportLogsServiceRequest{
				ResourceLogs: []*logspb.ResourceLogs{
					{
						ScopeLogs: []*logspb.ScopeLogs{
							{
								LogRecords: []*logspb.LogRecord{
									{
										TimeUnixNano: uint64(ingestTime.UnixNano()),
										Body: &commonpb.AnyValue{
											Value: &commonpb.AnyValue_StringValue{
												StringValue: "hello otlp logs",
											},
										},
										Attributes: []*commonpb.KeyValue{
											{
												Key: "test_id",
												Value: &commonpb.AnyValue{
													Value: &commonpb.AnyValue_StringValue{
														StringValue: testLabel,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(tests.VLPost(ctx, vlURL+"/insert/opentelemetry/v1/logs", "application/x-protobuf", payload, "otlp logs ingest")).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := tests.VLQuery(ctx, vlURL, fmt.Sprintf(`test_id:%q`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello otlp logs"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})
})

var _ = Describe("VLCluster", Label("vlcluster"), func() {
	var insertURL, selectURL string
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		namespace = tests.RandomNamespace("vlc")
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		vlclient := install.GetVMClient(t, kubeOpts)
		namePatch := tests.NewJSONPatchBuilder().Add("/metadata/name", namespace).MustBuild()
		install.InstallVLCluster(ctx, t, kubeOpts, namespace, vlclient, []jsonpatch.Patch{namePatch}, consts.VMClusterWaitTimeout)
		insertURL = consts.VLInsertUrl(namespace)
		selectURL = consts.VLSelectUrl(namespace)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		install.DeleteVLCluster(t, kubeOpts, namespace)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should ingest and query logs back",
		Label("id=8cfb59a7-cf4c-44f2-8951-6b6edd050add"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"hello vlcluster","test_id":%q}`,
				ingestTime.Format(time.RFC3339Nano), testLabel)

			Expect(tests.VLIngest(ctx, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := tests.VLQuery(ctx, selectURL, fmt.Sprintf(`{test_id=%q}`, testLabel),
					ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("hello vlcluster"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=5e9f0c3c-bf44-4179-af74-aa96e5ea1e2c"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)
			now := time.Now().UTC()
			payload := fmt.Sprintf(`{"_time":%q,"_msg":"stats test","test_id":%q}`,
				now.Format(time.RFC3339Nano), testLabel)

			Expect(tests.VLIngest(ctx, insertURL, "test_id", testLabel, []string{payload})).To(Succeed())

			Eventually(func(g Gomega) {
				body, err := tests.VLStatsCount(ctx, selectURL, `* | stats count() total`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring("total"))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

	It("should return 200 for a wildcard query",
		Label("id=5a2b45e9-5c58-4a09-a6b2-1aacb8bbe2f7"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			Eventually(func(g Gomega) {
				_, err := tests.VLQuery(ctx, selectURL, "*",
					time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})

})

var _ = Describe("VLCollector", Label("vlcollector"), func() {
	const (
		singleReleaseName    = "vl-col-single-test"
		collectorReleaseName = "vl-collector-test"
	)
	var vlURL string
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		namespace = tests.RandomNamespace("vlcol")
		vlURL = installVLSingle(ctx, namespace, singleReleaseName)
		installVLCollector(ctx, namespace, collectorReleaseName, consts.GetVLSingleSvc(singleReleaseName, namespace))
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		uninstallRelease(namespace, collectorReleaseName)
		uninstallRelease(namespace, singleReleaseName)
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("should collect pod logs and forward them to VLSingle",
		Label("id=d80de1a3-3ead-404f-8a53-536b7292d9ce"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			// Deploy a pod that emits a unique log line.
			testLabel := fmt.Sprintf("e2e-col-%s", namespace)
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			podName := "log-emitter"
			manifest, err := os.ReadFile(consts.LogEmitterYaml())
			Expect(err).NotTo(HaveOccurred())
			podYAML := strings.NewReplacer(
				"log-emitter-namespace", namespace,
				"log-emitter-message", testLabel,
			).Replace(string(manifest))

			By("Deploy log-emitter pod")
			install.KubectlApplyFromString(ctx, t, kubeOpts, podYAML)
			k8s.WaitUntilPodAvailable(t, kubeOpts, podName, 30, 5*time.Second)

			By("Wait for collector to ship the log line to VLSingle")
			Eventually(func(g Gomega) {
				body, err := tests.VLQuery(ctx, vlURL,
					fmt.Sprintf(`{kubernetes.pod_name=%q} %q`, podName, testLabel),
					time.Now().Add(-5*time.Minute), time.Now().Add(time.Minute))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(body)).To(ContainSubstring(testLabel))
			}, consts.ResourceWaitTimeout, consts.PollingInterval).Should(Succeed())
		})
})

var _ = Describe("VictoriaLogs Enterprise features", func() {
	var _ = Context("mTLS", func() {
		BeforeEach(func() {
			namespace = tests.RandomNamespace("vlent")
		})

		AfterEach(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailure(ctx, t, kubeOpts, namespace)
			install.DeleteVLCluster(t, kubeOpts, consts.DefaultVLClusterName)
			install.DeleteVLAgent(t, kubeOpts, "vlagent")
			install.DeleteVMAuth(t, kubeOpts, "vmauth")
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		It("should require mTLS for VictoriaLogs ingestion and querying",
			Label("enterprise", "id=2b9e4c3a-6f1d-4e8a-9c2b-7a5d8f1e3c6b"),
			SpecTimeout(consts.VLEnterpriseSpecTimeout),
			FlakeAttempts(3),
			func(ctx context.Context) {
				if consts.LicenseFile() == "" {
					Skip("VictoriaLogs mTLS is an Enterprise feature and requires a license; set --license-file")
				}

				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "vl-enterprise-test=true", "--overwrite")
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Generating mTLS certs")
				certs, err := newVLMTLSCerts(namespace)
				require.NoError(t, err)
				err = tests.NewSecretBuilder(vlMTLSSecretName).
					WithStringData("ca.crt", certs.caCert).
					WithStringData("server.crt", certs.serverCert).
					WithStringData("server.key", certs.serverKey).
					WithStringData("client.crt", certs.clientCert).
					WithStringData("client.key", certs.clientKey).
					Apply(ctx, t, kubeOpts)
				require.NoError(t, err)

				By("Installing VLCluster with every component protected by mTLS")
				patches := []jsonpatch.Patch{
					fullVLMTLSClusterPatch(),
					tests.NewJSONPatchBuilder().Add("/spec/clusterVersion", consts.VLEnterpriseVersion()).MustBuild(),
				}
				install.InstallVLCluster(ctx, t, kubeOpts, namespace, vmclient, patches, consts.PollingTimeout)

				insertURL := fmt.Sprintf("https://%s", consts.GetVLClusterInsertSvc(consts.DefaultVLClusterName, namespace))
				vmAuthURL := fmt.Sprintf("http://vmauth-vmauth.%s.svc.cluster.local:8427", namespace)

				By("Deploying VLAgent with mTLS remote write")
				install.InstallVLAgent(ctx, t, kubeOpts, namespace, vmclient,
					insertURL+"/insert/native", vlMTLSSecretName)

				By("Deploying VMAuth with VLAgent and VLSelect routes")
				err = tests.NewSecretBuilder(vmAuthConfigSecret).
					WithStringData("config.yaml", vmAuthConfig(namespace)).
					Apply(ctx, t, kubeOpts)
				require.NoError(t, err)
				vmauthPatches := []jsonpatch.Patch{
					tests.NewJSONPatchBuilder().Add("/spec/configSecret", vmAuthConfigSecret).MustBuild(),
					tests.NewJSONPatchBuilder().Add("/spec/secrets", []string{vlMTLSSecretName}).MustBuild(),
				}
				install.InstallVMAuth(ctx, t, kubeOpts, namespace, vmclient, vmauthPatches)

				By("Deploying a curl pod with the mTLS secret mounted")
				tests.InstallVLCurlPod(ctx, t, kubeOpts, vlMTLSClientPodName, vlMTLSSecretName)

				pipelineLabel := fmt.Sprintf("e2e-mtls-pipeline-%s", namespace)
				pipelinePayload := fmt.Sprintf(`{"_msg":"hello pipeline mtls","test_id":%q}`+"\n", pipelineLabel)

				By("Ingestion through VMAuth must traverse VLAgent over mTLS")
				_, err = runVMAuthCurlIngest(ctx, t, kubeOpts, vmAuthURL, pipelinePayload)
				require.NoError(t, err)

				tests.WaitForDataPropagation()

				By("Querying through VMAuth must return the ingested data")
				Eventually(func(g Gomega) {
					out, err := runVMAuthCurlQuery(ctx, t, kubeOpts, vmAuthURL, fmt.Sprintf(`test_id:%q`, pipelineLabel))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(ContainSubstring("hello pipeline mtls"))
				}, consts.PollingInterval*3, consts.PollingInterval).Should(Succeed())
			})
	})
})

// vlTCPProbe builds a TCP-socket probe for the given port, bypassing the mTLS
// handshake that a plain HTTP health check would otherwise fail.
func vlTCPProbe(port int) map[string]interface{} {
	return map[string]interface{}{"tcpSocket": map[string]interface{}{"port": port}}
}

func vmAuthConfig(namespace string) string {
	return fmt.Sprintf(`unauthorized_user:
  tls_ca_file: /etc/vm/secrets/%s/ca.crt
  tls_cert_file: /etc/vm/secrets/%s/client.crt
  tls_key_file: /etc/vm/secrets/%s/client.key
  url_map:
  - src_paths:
    - "/insert/.*"
    url_prefix: "http://%s"
  - src_paths:
    - "/select/.*"
    url_prefix: "https://%s"

`, vlMTLSSecretName, vlMTLSSecretName, vlMTLSSecretName,
		consts.GetVLAgentSvc("vlagent", namespace), consts.GetVLClusterSelectSvc(consts.DefaultVLClusterName, namespace))
}

// fullVLMTLSClusterPatch secures public and internal VLCluster traffic with mTLS.
func fullVLMTLSClusterPatch() jsonpatch.Patch {
	secretPath := "/etc/vm/secrets/" + vlMTLSSecretName
	httpArgs := map[string]string{
		"tls": "true", "tlsCertFile": secretPath + "/server.crt", "tlsKeyFile": secretPath + "/server.key",
		"mtls": "true", "mtlsCAFile": secretPath + "/ca.crt",
	}
	// vlstorage.replicaCount is 2 (manifests/overwatch/vlcluster.yaml); these are
	// array-type flags positionally aligned with the -storageNode address list, and
	// unspecified positions default to false/empty rather than broadcasting a single
	// value to all entries, so every value must be duplicated once per storage node.
	storageNodeArgs := map[string]string{
		"storageNode.tls":         "true,true",
		"storageNode.tlsCertFile": secretPath + "/client.crt," + secretPath + "/client.crt",
		"storageNode.tlsKeyFile":  secretPath + "/client.key," + secretPath + "/client.key",
		"storageNode.tlsCAFile":   secretPath + "/ca.crt," + secretPath + "/ca.crt",
	}

	insertArgs := map[string]string{}
	for k, v := range httpArgs {
		insertArgs[k] = v
	}
	delete(insertArgs, "mtls")
	delete(insertArgs, "mtlsCAFile")
	for k, v := range storageNodeArgs {
		insertArgs[k] = v
	}
	selectArgs := map[string]string{}
	for k, v := range httpArgs {
		selectArgs[k] = v
	}
	delete(selectArgs, "mtls")
	delete(selectArgs, "mtlsCAFile")
	for k, v := range storageNodeArgs {
		selectArgs[k] = v
	}
	storageArgs := map[string]string{
		"tls":         httpArgs["tls"],
		"tlsCertFile": httpArgs["tlsCertFile"],
		"tlsKeyFile":  httpArgs["tlsKeyFile"],
	}

	return tests.NewJSONPatchBuilder().
		Add("/spec/vlinsert/secrets", []string{vlMTLSSecretName}).
		Add("/spec/vlinsert/extraArgs", insertArgs).
		Add("/spec/vlinsert/readinessProbe", vlTCPProbe(9481)).
		Add("/spec/vlinsert/livenessProbe", vlTCPProbe(9481)).
		Add("/spec/vlinsert/startupProbe", vlTCPProbe(9481)).
		Add("/spec/vlselect/secrets", []string{vlMTLSSecretName}).
		Add("/spec/vlselect/extraArgs", selectArgs).
		Add("/spec/vlselect/readinessProbe", vlTCPProbe(9471)).
		Add("/spec/vlselect/livenessProbe", vlTCPProbe(9471)).
		Add("/spec/vlselect/startupProbe", vlTCPProbe(9471)).
		Add("/spec/vlstorage/secrets", []string{vlMTLSSecretName}).
		Add("/spec/vlstorage/extraArgs", storageArgs).
		Add("/spec/vlstorage/readinessProbe", vlTCPProbe(9491)).
		Add("/spec/vlstorage/livenessProbe", vlTCPProbe(9491)).
		Add("/spec/vlstorage/startupProbe", vlTCPProbe(9491)).
		MustBuild()
}

func runVMAuthCurlIngest(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmAuthURL, payload string) (string, error) {
	return tests.RunVLCurl(ctx, t, kubeOpts, vlMTLSClientPodName, vmAuthURL+"/insert/jsonline?_stream_fields=test_id", false,
		"--header", "Content-Type: application/stream+json", "--data-binary", payload)
}

func runVMAuthCurlQuery(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmAuthURL, query string) (string, error) {
	now := time.Now().UTC()
	return tests.RunVLCurl(ctx, t, kubeOpts, vlMTLSClientPodName, vmAuthURL+"/select/logsql/query", false,
		"--data-urlencode", "query="+query,
		"--data-urlencode", "start="+now.Add(-5*time.Minute).Format(time.RFC3339),
		"--data-urlencode", "end="+now.Add(time.Minute).Format(time.RFC3339),
	)
}

type vlMTLSCerts struct {
	caCert     string
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
}

// newVLMTLSCerts generates a short-lived CA plus a server cert (covering vlinsert,
// vlselect, and vlstorage's per-pod headless-service hostnames in the given
// namespace) and a client cert, all signed by that CA.
func newVLMTLSCerts(namespace string) (vlMTLSCerts, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return vlMTLSCerts{}, err
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "vl-mtls-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(cryptorand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return vlMTLSCerts{}, err
	}

	clusterName := consts.DefaultVLClusterName
	serverCert, serverKey, err := newVLSignedCert(ca, caKey, "victoria-logs-cluster",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, []string{
			fmt.Sprintf("vlinsert-%s.%s.svc.cluster.local", clusterName, namespace),
			fmt.Sprintf("vlinsert-%s.%s.svc", clusterName, namespace),
			fmt.Sprintf("vlinsert-%s.%s", clusterName, namespace),
			fmt.Sprintf("vlselect-%s.%s.svc.cluster.local", clusterName, namespace),
			fmt.Sprintf("vlselect-%s.%s.svc", clusterName, namespace),
			fmt.Sprintf("vlselect-%s.%s", clusterName, namespace),
			fmt.Sprintf("vlstorage-%s.%s.svc.cluster.local", clusterName, namespace),
			fmt.Sprintf("vlstorage-%s.%s.svc", clusterName, namespace),
			fmt.Sprintf("vlstorage-%s.%s", clusterName, namespace),
			fmt.Sprintf("*.vlstorage-%s.%s.svc.cluster.local", clusterName, namespace),
			fmt.Sprintf("*.vlstorage-%s.%s.svc", clusterName, namespace),
			fmt.Sprintf("*.vlstorage-%s.%s", clusterName, namespace),
		})
	if err != nil {
		return vlMTLSCerts{}, err
	}
	clientCert, clientKey, err := newVLSignedCert(ca, caKey, "vl-client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
	if err != nil {
		return vlMTLSCerts{}, err
	}

	return vlMTLSCerts{
		caCert:     encodeVLCert(caDER),
		serverCert: serverCert,
		serverKey:  serverKey,
		clientCert: clientCert,
		clientKey:  clientKey,
	}, nil
}

func newVLSignedCert(ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, usages []x509.ExtKeyUsage, dnsNames []string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
		DNSNames:     dnsNames,
	}
	certDER, err := x509.CreateCertificate(cryptorand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return encodeVLCert(certDER), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), nil
}

func encodeVLCert(certDER []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
}
