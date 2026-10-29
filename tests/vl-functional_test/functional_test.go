package vl_functional_test

import (
	"context"
	"fmt"
	"maps"
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
	statsCountQuery     = `* | stats count() total`
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

		// Install VPA + Gateway API CRDs before the operator starts, so its RESTMapper
		// discovers these Kinds at boot instead of hard-failing reconciles with
		// `no matches for kind "VerticalPodAutoscaler"` until its cache refreshes.
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.EnsureVPACRDs(ctx, t, kubeOpts)
		install.EnsureGatewayAPICRDs(ctx, t, kubeOpts)

		// Clean up stale namespaces left by previous killed enterprise runs.
		if consts.LicenseFile() != "" {
			tests.CleanupStaleNamespaces(ctx, t, kubeOpts, "vl-enterprise-test=true")
		}

		install.DiscoverIngressHost(ctx, t)
		tests.InstallVMStackAndGather(ctx, t)

		// Overwatch needs the CRDs and "vmks" CRs installed by the stage above.
		tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})
	},
	func(ctx context.Context) {
		t = tests.GetT()
	},
)

// helmInstall upgrades or installs a chart into ns, failing the test on error.
func helmInstall(ns, chart, release, chartVersion, valuesFile string, setValues, setFiles map[string]string) {
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if chartVersion != "" {
		upgradeArgs = append(upgradeArgs, "--version", chartVersion)
	}
	opts := &helm.Options{
		KubectlOptions: k8s.NewKubectlOptions("", "", ns),
		ValuesFiles:    []string{valuesFile},
		SetFiles:       setFiles,
		SetValues:      setValues,
		ExtraArgs:      map[string][]string{"upgrade": upgradeArgs},
	}

	By(fmt.Sprintf("Install %s as %s in %s", chart, release, ns))
	if err := helm.UpgradeE(t, opts, chart, release); err != nil {
		t.Fatalf("failed to install %s: %v", chart, err)
	}
}

// installVLSingle installs victoria-logs-single in the given namespace and returns its ingress URL.
func installVLSingle(ctx context.Context, ns, releaseName string) string {
	setValues := map[string]string{
		"server.ingress.enabled":          "true",
		"server.ingress.ingressClassName": "traefik",
		"server.ingress.hosts[0].name":    consts.VLNamespacedHost(ns),
		"server.ingress.hosts[0].path[0]": "/",
	}
	if v := consts.VLVersion(); v != "" {
		setValues["server.image.tag"] = v
	}
	helmInstall(ns, consts.VictoriaLogsSingleChart, releaseName, consts.VLSingleChartVersion(),
		consts.VictoriaLogsSingleValuesFile(), setValues, install.VLSingleLicenseFiles())

	vlURL := consts.VLUrl(ns)
	helpers.WaitForHTTPRoute(ctx, t, vlURL+"/health")
	return vlURL
}

// installVLCollector installs victoria-logs-collector pointed at the given VLSingle insert URL.
func installVLCollector(ns, releaseName, vlSingleAddr string) {
	helmInstall(ns, consts.VictoriaLogsCollectorChart, releaseName, consts.VLCollectorChartVersion(),
		consts.VictoriaLogsCollectorValuesFile(),
		map[string]string{"remoteWrite[0].url": "http://" + vlSingleAddr}, nil)
}

// uninstallRelease removes a Helm release from the namespace.
func uninstallRelease(ns, releaseName string) {
	opts := &helm.Options{KubectlOptions: k8s.NewKubectlOptions("", "", ns)}
	By(fmt.Sprintf("Uninstall %s from %s", releaseName, ns))
	_ = helm.DeleteE(t, opts, releaseName, true)
}

// logLine builds a single JSON log record tagged with test_id.
func logLine(ts time.Time, msg, testLabel string) string {
	return fmt.Sprintf(`{"_time":%q,"_msg":%q,"test_id":%q}`, ts.Format(time.RFC3339Nano), msg, testLabel)
}

// pollUntilReady wraps a polling function with the shared resource-wait budget.
func pollUntilReady(ctx context.Context, poll interface{}) AsyncAssertion {
	return Eventually(ctx, poll).
		WithTimeout(consts.ResourceWaitTimeout).
		WithPolling(consts.PollingInterval)
}

// logsFor yields the body of query run against url, from ingestTime until now.
func logsFor(ctx context.Context, url, query string, ingestTime time.Time) AsyncAssertion {
	return pollUntilReady(ctx, func() (string, error) {
		body, err := tests.VLQuery(ctx, url, query, ingestTime.Add(-time.Second), time.Now().UTC().Add(time.Second))
		return string(body), err
	})
}

// statsFor yields the body of a /select/logsql/stats_query request against url.
func statsFor(ctx context.Context, url string) AsyncAssertion {
	return pollUntilReady(ctx, func() (string, error) {
		body, err := tests.VLStatsCount(ctx, url, statsCountQuery)
		return string(body), err
	})
}

// wildcardQuery yields the error of a "*" query against url.
func wildcardQuery(ctx context.Context, url string) AsyncAssertion {
	return pollUntilReady(ctx, func() error {
		_, err := tests.VLQuery(ctx, url, "*", time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
		return err
	})
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

			Expect(tests.VLIngest(ctx, vlURL, "test_id", testLabel,
				[]string{logLine(ingestTime, "hello vlsingle", testLabel)})).To(Succeed())

			logsFor(ctx, vlURL, fmt.Sprintf(`{test_id=%q}`, testLabel), ingestTime).Should(ContainSubstring("hello vlsingle"))
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=338d24c1-7639-42e6-b9a1-9d99d6dac6d3"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)

			Expect(tests.VLIngest(ctx, vlURL, "test_id", testLabel,
				[]string{logLine(time.Now().UTC(), "stats test", testLabel)})).To(Succeed())

			statsFor(ctx, vlURL).Should(ContainSubstring("total"))
		})

	It("should return 200 for a wildcard query",
		Label("id=f3a6ea21-081e-4f72-84c8-80fbca451cf0"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			wildcardQuery(ctx, vlURL).Should(Succeed())
		})

	It("should ingest Elasticsearch bulk logs",
		Label("id=6d4f6357-92d6-4db5-a42c-f8f3c02f8668"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-es-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := "{\"create\":{}}\n" + logLine(ingestTime, "hello elasticsearch bulk", testLabel) + "\n"

			Expect(tests.VLPost(ctx, vlURL+"/insert/elasticsearch/_bulk", "application/json", []byte(payload), "elasticsearch bulk ingest")).To(Succeed())

			logsFor(ctx, vlURL, fmt.Sprintf(`test_id:%q`, testLabel), ingestTime).Should(ContainSubstring("hello elasticsearch bulk"))
		})

	It("should ingest Loki push logs",
		Label("id=cfdd5ac7-7080-4974-8fdd-0f6e6f6a8263"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-loki-%s", namespace)
			ingestTime := time.Now().UTC()
			payload := fmt.Sprintf(`{"streams":[{"stream":{"test_id":%q,"job":"e2e"},"values":[["%d","hello loki push"]]}]}`,
				testLabel, ingestTime.UnixNano())

			Expect(tests.VLPost(ctx, vlURL+"/insert/loki/api/v1/push", "application/json", []byte(payload), "loki push ingest")).To(Succeed())

			logsFor(ctx, vlURL, fmt.Sprintf(`{test_id=%q} "hello loki push"`, testLabel), ingestTime).Should(ContainSubstring("hello loki push"))
		})

	It("should ingest OTLP logs",
		Label("id=f1692c65-93b0-420d-88b9-952b38c35012"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-otlp-%s", namespace)
			ingestTime := time.Now().UTC()

			payload, err := proto.Marshal(&collogspb.ExportLogsServiceRequest{
				ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(ingestTime.UnixNano()),
					Body:         otlpString("hello otlp logs"),
					Attributes:   []*commonpb.KeyValue{{Key: "test_id", Value: otlpString(testLabel)}},
				}}}}}},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(tests.VLPost(ctx, vlURL+"/insert/opentelemetry/v1/logs", "application/x-protobuf", payload, "otlp logs ingest")).To(Succeed())

			logsFor(ctx, vlURL, fmt.Sprintf(`test_id:%q`, testLabel), ingestTime).Should(ContainSubstring("hello otlp logs"))
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

			Expect(tests.VLIngest(ctx, insertURL, "test_id", testLabel,
				[]string{logLine(ingestTime, "hello vlcluster", testLabel)})).To(Succeed())

			logsFor(ctx, selectURL, fmt.Sprintf(`{test_id=%q}`, testLabel), ingestTime).Should(ContainSubstring("hello vlcluster"))
		})

	It("should return stats via /select/logsql/stats_query",
		Label("id=5e9f0c3c-bf44-4179-af74-aa96e5ea1e2c"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			testLabel := fmt.Sprintf("e2e-stats-%s", namespace)

			Expect(tests.VLIngest(ctx, insertURL, "test_id", testLabel,
				[]string{logLine(time.Now().UTC(), "stats test", testLabel)})).To(Succeed())

			statsFor(ctx, selectURL).Should(ContainSubstring("total"))
		})

	It("should return 200 for a wildcard query",
		Label("id=5a2b45e9-5c58-4a09-a6b2-1aacb8bbe2f7"),
		SpecTimeout(consts.VLFunctionalSpecTimeout),
		func(ctx context.Context) {
			wildcardQuery(ctx, selectURL).Should(Succeed())
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
		installVLCollector(namespace, collectorReleaseName, consts.GetVLSingleSvc(singleReleaseName, namespace))
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

		PIt("should require mTLS for VictoriaLogs ingestion and querying",
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
				require.NoError(t, certs.ApplySecret(ctx, t, kubeOpts, vlMTLSSecretName))

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

func otlpString(value string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
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

	// vlinsert/vlselect serve TLS without demanding a client cert, but present one
	// to vlstorage; vlstorage serves TLS only.
	frontendArgs := maps.Clone(httpArgs)
	delete(frontendArgs, "mtls")
	delete(frontendArgs, "mtlsCAFile")
	maps.Copy(frontendArgs, storageNodeArgs)
	storageArgs := map[string]string{
		"tls":         httpArgs["tls"],
		"tlsCertFile": httpArgs["tlsCertFile"],
		"tlsKeyFile":  httpArgs["tlsKeyFile"],
	}

	return tests.NewJSONPatchBuilder().
		Add("/spec/vlinsert/secrets", []string{vlMTLSSecretName}).
		Add("/spec/vlinsert/extraArgs", frontendArgs).
		Add("/spec/vlinsert/readinessProbe", tests.TCPProbe(9481)).
		Add("/spec/vlinsert/livenessProbe", tests.TCPProbe(9481)).
		Add("/spec/vlinsert/startupProbe", tests.TCPProbe(9481)).
		Add("/spec/vlselect/secrets", []string{vlMTLSSecretName}).
		Add("/spec/vlselect/extraArgs", frontendArgs).
		Add("/spec/vlselect/readinessProbe", tests.TCPProbe(9471)).
		Add("/spec/vlselect/livenessProbe", tests.TCPProbe(9471)).
		Add("/spec/vlselect/startupProbe", tests.TCPProbe(9471)).
		Add("/spec/vlstorage/secrets", []string{vlMTLSSecretName}).
		Add("/spec/vlstorage/extraArgs", storageArgs).
		Add("/spec/vlstorage/readinessProbe", tests.TCPProbe(9491)).
		Add("/spec/vlstorage/livenessProbe", tests.TCPProbe(9491)).
		Add("/spec/vlstorage/startupProbe", tests.TCPProbe(9491)).
		MustBuild()
}

// newVLMTLSCerts issues a CA plus server cert covering vlinsert, vlselect and
// vlstorage's per-pod headless-service hostnames, and a matching client cert.
func newVLMTLSCerts(namespace string) (tests.MTLSCerts, error) {
	cluster := consts.DefaultVLClusterName
	return tests.NewMTLSCerts("vl-mtls-ca", "victoria-logs-cluster", "vl-client",
		tests.ServiceDNSNames(namespace,
			"vlinsert-"+cluster, "vlselect-"+cluster, "vlstorage-"+cluster, "*.vlstorage-"+cluster))
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
