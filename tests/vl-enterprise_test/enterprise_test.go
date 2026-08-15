package vl_enterprise_test

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
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
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

func TestVLEnterpriseTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "VictoriaLogs Enterprise test Suite", suiteConfig, reporterConfig)
}

var (
	t         terratesting.TestingT
	namespace string
)

// Install shared infra for the first process, propagate t to the rest.
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()

		// Clean up stale namespaces from previous killed runs.
		defaultKubeOpts := k8s.NewKubectlOptions("", "", "default")
		tests.CleanupStaleNamespaces(ctx, t, defaultKubeOpts, "vl-enterprise-test=true")

		// Stage 1: install VPA + Gateway API CRDs before the operator starts. Doing this
		// first (not after InstallVMStackAndGather) means the operator's own RESTMapper
		// discovers these Kinds at boot instead of racing a CRD applied after it is already
		// running - that race made the operator hard-fail reconciles with
		// `no matches for kind "VerticalPodAutoscaler"` until its cache eventually refreshed.
		install.EnsureVPACRDs(ctx, t, defaultKubeOpts)
		install.EnsureGatewayAPICRDs(ctx, t, defaultKubeOpts)

		install.DiscoverIngressHost(ctx, t)

		// Stage 2 (parallel): vmgather + vm k8s stack + victorialogs single + collector (all need nginx host).
		tests.InstallVMStackAndGather(ctx, t)

		// Stage 3: install overwatch. Needs the VMAgent/VMAlert/VMSingle CRDs and the
		// "vmks" CRs installed by InstallVMK8StackWithHelm above.
		tests.InstallOverwatchStage(ctx, t, tests.OverwatchStageOptions{})
	},
	func(ctx context.Context) {
		t = tests.GetT()
		namespace = tests.RandomNamespace("vlent")
	},
)

var _ = Describe("VictoriaLogs Enterprise features", func() {
	var _ = Context("mTLS", func() {
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
