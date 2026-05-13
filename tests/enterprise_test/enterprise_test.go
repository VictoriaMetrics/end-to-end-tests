package enterprise_test

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
	"net"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

const mtlsSecretName = "vm-mtls"

type mtlsCerts struct {
	caCert     string
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
}

func TestEnterpriseTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Enterprise test Suite", suiteConfig, reporterConfig)
}

var (
	t         terratesting.TestingT
	namespace string
	overwatch promquery.PrometheusClient
)

// Install shared infra for the first process, set namespace for the rest.
var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) {
		t = tests.GetT()
		install.DiscoverIngressHost(ctx, t)
		install.InstallVMGather(t)
		install.InstallVMK8StackWithHelm(
			context.Background(),
			consts.VMK8sStackChart,
			consts.SmokeValuesFile(),
			t,
			consts.DefaultVMNamespace,
			consts.DefaultReleaseName,
		)
		install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)
		install.InstallStrimziOperator(ctx, t, consts.KafkaNamespace)

		// Remove stock VMCluster - it will be recreated per-test namespace
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		install.DeleteVMCluster(t, kubeOpts, consts.DefaultReleaseName)
	},
	func(ctx context.Context) {
		t = tests.GetT()
		namespace = tests.RandomNamespace("vm")
	},
)

var _ = Describe("VMAgent Enterprise features", func() {

	var _ = Context("Kafka", func() {
		BeforeEach(func(ctx context.Context) {
			var err error
			overwatch, err = tests.SetupOverwatchClient(ctx, t)
			require.NoError(t, err)
		})

		AfterEach(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)
			install.DeleteVMAgent(t, kubeOpts, "vmagent-producer")
			install.DeleteVMAgent(t, kubeOpts, "vmagent")
			install.DeleteKafka(t, kubeOpts)
			install.DeleteVMCluster(t, kubeOpts, consts.DefaultVMClusterName)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		It("should ingest metrics via Kafka topic",
			Label("enterprise", "id=53a1327f-e029-4a09-aa3d-01d8580fd633"),
			func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				vmclient := install.GetVMClient(t, kubeOpts)

				var licensePatch jsonpatch.Patch
				if consts.LicenseFile() != "" {
					var err error
					secretYaml, err := consts.PrepareLicenseSecret(namespace)
					require.NoError(t, err)
					k8s.KubectlApplyFromString(t, kubeOpts, secretYaml)
					licensePatch, err = jsonpatch.DecodePatch([]byte(fmt.Sprintf(
						`[{"op":"add","path":"/spec/license","value":{"keyRef":{"name":%q,"key":%q}}}]`,
						consts.LicenseSecretName, consts.LicenseSecretKey,
					)))
					require.NoError(t, err)
				}

				By("Installing VMCluster in test namespace")
				install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{licensePatch})

				By("Installing Kafka cluster in test namespace")
				install.InstallKafka(ctx, t, kubeOpts, namespace)

				brokerAddr := install.KafkaBrokerAddr(namespace)
				vmInsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write",
					consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))

				By("Deploying producer VMAgent (relays remote write data to Kafka)")
				producerPatches := append([]jsonpatch.Patch{
					tests.NewJSONPatchBuilder().
						Replace("/metadata/name", "vmagent-producer").
						Add("/spec/remoteWrite", []map[string]interface{}{
							{"url": fmt.Sprintf("kafka://%s/?topic=metrics", brokerAddr)},
						}).
						MustBuild(),
				}, licensePatch)
				install.ApplyVMAgentWithPatches(ctx, t, kubeOpts, namespace, vmclient, "vmagent-producer", producerPatches)

				By("Deploying consumer VMAgent (reads from Kafka, forwards to VMCluster)")
				consumerPatches := append([]jsonpatch.Patch{
					tests.NewJSONPatchBuilder().
						Add("/spec/remoteWrite", []map[string]interface{}{
							{"url": vmInsertURL},
						}).
						WithExtraArg("kafka.consumer.topic", "metrics").
						WithExtraArg("kafka.consumer.topic.brokers", brokerAddr).
						WithExtraArg("kafka.consumer.topic.format", "promremotewrite").
						WithExtraArg("kafka.consumer.topic.groupID", "vmagent-consumer").
						WithExtraArg("kafka.consumer.topic.options", "auto.offset.reset=earliest").
						MustBuild(),
				}, licensePatch)
				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, consumerPatches)

				By("Waiting for Kafka consumer to connect to brokers")
				require.Eventually(t, func() bool {
					out, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts,
						"exec", "deploy/vmagent-vmagent", "-c", "vmagent", "--",
						"sh", "-c",
						"wget -qO- http://localhost:8429/metrics | grep vmagent_kafka_consumer_brokers_up")
					if err != nil {
						return false
					}
					for _, line := range strings.Split(out, "\n") {
						if strings.HasPrefix(line, "vmagent_kafka_consumer_brokers_up{") &&
							!strings.HasSuffix(strings.TrimSpace(line), "} 0") {
							return true
						}
					}
					return false
				}, consts.ResourceWaitTimeout, consts.PollingInterval, "kafka consumer not connected to brokers")

				By("Remote-writing test metrics to producer VMAgent")
				producerURL := tests.VMAgentNamedRemoteWriteURL("vmagent-producer", namespace)
				ts := tests.NewTimeSeriesBuilder("kafka_test").
					WithCount(10).
					WithValue(42).
					WithLabel("source", "kafka").
					Build()
				remoteWriter := tests.NewRemoteWriteBuilder().WithURL(producerURL)
				err := remoteWriter.Send(ts)
				require.NoError(t, err)

				tests.WaitForDataPropagation()

				By("Verifying metrics from Kafka appear in VMCluster")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "kafka_test_0", consts.Retries)
				require.NoError(t, err)
				tests.NewScannedMetric(t, value, "kafka_test_0").EqualTo(model.SampleValue(42))
				require.Equal(t, labels["source"], model.LabelValue("kafka"))
			})
	})

	var _ = Context("VMSingle", func() {
		BeforeEach(func(ctx context.Context) {
			var err error
			overwatch, err = tests.SetupOverwatchClient(ctx, t)
			require.NoError(t, err)
		})

		AfterEach(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)
			install.DeleteVMSingle(t, kubeOpts, "vmsingle")
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		Describe("Downsampling", func() {
			It("should downsample data", Label("enterprise", "id=6028448d-69e3-4c55-83f2-111122223333"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Configure VMSingle with downsampling")
				patch := tests.NewJSONPatchBuilder().
					WithExtraArg("downsampling.period", "0s:1m").
					MustBuild()

				install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})

				By("Inserting multiple samples")
				remoteWriter := tests.NewRemoteWriteBuilder().ForVMSingle(namespace)
				for i := 0; i < 5; i++ {
					ts := tests.NewTimeSeriesBuilder("downsample_test").
						WithCount(1).
						WithValue(float64(i)).
						Build()
					err := remoteWriter.Send(ts)
					require.NoError(t, err)
					time.Sleep(time.Second)
				}

				time.Sleep(time.Minute)

				By("Verifying data is downsampled")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "count_over_time(downsample_test_0[5m])", 5)
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(1), value, "Expected one sample after downsampling")
			})
		})

		Describe("Retention Filters", func() {
			It("should apply retention filters", Label("enterprise", "id=7028448d-69e3-4c55-83f2-111122223333"), func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				vmclient := install.GetVMClient(t, kubeOpts)

				By("Configure VMSingle with retention filters")
				patch := tests.NewJSONPatchBuilder().
					WithExtraArg("retentionFilter", `{drop="true"}:5s`).
					MustBuild()

				install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, []jsonpatch.Patch{patch})

				By("Inserting data")
				remoteWriter := tests.NewRemoteWriteBuilder().ForVMSingle(namespace)
				tsDrop := tests.NewTimeSeriesBuilder("retention_drop").
					WithCount(1).
					WithValue(1).
					WithLabel("drop", "true").
					Build()
				tsKeep := tests.NewTimeSeriesBuilder("retention_keep").
					WithCount(1).
					WithValue(1).
					WithLabel("drop", "false").
					Build()

				err := remoteWriter.Send(tsDrop)
				require.NoError(t, err)
				err = remoteWriter.Send(tsKeep)
				require.NoError(t, err)

				By("Wait for time to pass and trigger retention")
				time.Sleep(time.Minute)

				By("Verifying data")
				prom := tests.NewPromClientBuilder().
					ForVMSingle(namespace).
					WithStartTime(overwatch.Start).
					MustBuild()

				_, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "retention_drop_0", 5)
				require.EqualError(t, err, consts.ErrNoDataReturned)
				require.Equal(t, model.SampleValue(0), value)

				_, value, err = prom.VectorScan(ctx, "retention_keep_0")
				require.NoError(t, err)
				require.Equal(t, model.SampleValue(1), value)
			})
		})
	})

	var _ = Context("mTLS", func() {
		BeforeEach(func(ctx context.Context) {
			var err error
			overwatch, err = tests.SetupOverwatchClient(ctx, t)
			require.NoError(t, err)
		})

		AfterEach(func(ctx context.Context) {
			kubeOpts := k8s.NewKubectlOptions("", "", namespace)
			tests.GatherOnFailure(ctx, t, kubeOpts, namespace, consts.DefaultReleaseName)
			install.DeleteVMAgent(t, kubeOpts, "vmagent-no-client-cert")
			install.DeleteVMAgent(t, kubeOpts, "vmagent")
			install.DeleteVMCluster(t, kubeOpts, consts.DefaultVMClusterName)
			tests.CleanupNamespace(t, kubeOpts, namespace)
		})

		It("should require mTLS for VMAgent remote write to VMCluster",
			Label("enterprise", "id=1ad209d2-2f85-47e3-ae7f-427b687e7f31"),
			func(ctx context.Context) {
				kubeOpts := k8s.NewKubectlOptions("", "", namespace)
				tests.EnsureNamespaceExists(t, kubeOpts, namespace)
				vmclient := install.GetVMClient(t, kubeOpts)

				certs, err := newMTLSCerts(namespace)
				require.NoError(t, err)
				err = tests.NewSecretBuilder(mtlsSecretName).
					WithStringData("ca.crt", certs.caCert).
					WithStringData("server.crt", certs.serverCert).
					WithStringData("server.key", certs.serverKey).
					WithStringData("client.crt", certs.clientCert).
					WithStringData("client.key", certs.clientKey).
					Apply(t, kubeOpts)
				require.NoError(t, err)

				licensePatch := enterpriseLicensePatch(kubeOpts)
				vmInsertURL := fmt.Sprintf("https://%s/insert/0/prometheus/api/v1/write",
					consts.GetVMInsertSvc(consts.DefaultVMClusterName, namespace))
				serverName := fmt.Sprintf("vminsert-%s.%s.svc.cluster.local", consts.DefaultVMClusterName, namespace)

				By("Installing VMCluster with mTLS enabled for vminsert")
				clusterPatches := enterprisePatches(licensePatch,
					tests.NewJSONPatchBuilder().
						Add("/spec/vminsert/secrets", []string{mtlsSecretName}).
						Add("/spec/vminsert/extraArgs", map[string]string{
							"tls":         "true",
							"tlsCertFile": "/etc/vm/secrets/" + mtlsSecretName + "/server.crt",
							"tlsKeyFile":  "/etc/vm/secrets/" + mtlsSecretName + "/server.key",
							"mtls":        "true",
							"mtlsCAFile":  "/etc/vm/secrets/" + mtlsSecretName + "/ca.crt",
						}).
						MustBuild(),
				)
				install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, clusterPatches)

				By("Deploying VMAgent without client certificate")
				badPatches := enterprisePatches(licensePatch,
					tests.NewJSONPatchBuilder().
						Replace("/metadata/name", "vmagent-no-client-cert").
						Add("/spec/remoteWrite", []map[string]interface{}{
							{
								"url": vmInsertURL,
								"tlsConfig": map[string]interface{}{
									"ca":         map[string]interface{}{"secret": map[string]string{"name": mtlsSecretName, "key": "ca.crt"}},
									"serverName": serverName,
								},
							},
						}).
						MustBuild(),
				)
				install.ApplyVMAgentWithPatches(ctx, t, kubeOpts, namespace, vmclient, "vmagent-no-client-cert", badPatches)

				By("Remote-writing metrics to VMAgent without client certificate")
				badTS := tests.NewTimeSeriesBuilder("mtls_rejected").
					WithCount(1).
					WithValue(13).
					WithLabel("source", "mtls").
					Build()
				err = tests.NewRemoteWriteBuilder().
					WithURL(tests.VMAgentNamedRemoteWriteURL("vmagent-no-client-cert", namespace)).
					Send(badTS)
				require.NoError(t, err)

				By("Deploying VMAgent with client certificate")
				goodPatches := enterprisePatches(licensePatch,
					tests.NewJSONPatchBuilder().
						Add("/spec/secrets", []string{mtlsSecretName}).
						Add("/spec/remoteWrite", []map[string]interface{}{
							{
								"url": vmInsertURL,
								"tlsConfig": map[string]interface{}{
									"ca":         map[string]interface{}{"secret": map[string]string{"name": mtlsSecretName, "key": "ca.crt"}},
									"cert":       map[string]interface{}{"secret": map[string]string{"name": mtlsSecretName, "key": "client.crt"}},
									"keySecret":  map[string]string{"name": mtlsSecretName, "key": "client.key"},
									"serverName": serverName,
								},
							},
						}).
						MustBuild(),
				)
				install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, goodPatches)

				By("Remote-writing metrics to VMAgent with client certificate")
				goodTS := tests.NewTimeSeriesBuilder("mtls_accepted").
					WithCount(1).
					WithValue(42).
					WithLabel("source", "mtls").
					Build()
				err = tests.NewRemoteWriteBuilder().
					WithURL(tests.VMAgentRemoteWriteURL(namespace)).
					Send(goodTS)
				require.NoError(t, err)

				tests.WaitForDataPropagation()

				By("Verifying only client-certified VMAgent writes reach VMCluster")
				prom := tests.NewPromClientBuilder().
					WithNamespace(namespace).
					WithTenant(0).
					WithStartTime(overwatch.Start).
					MustBuild()

				labels, value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "mtls_accepted_0", consts.Retries)
				require.NoError(t, err)
				tests.NewScannedMetric(t, value, "mtls_accepted_0").EqualTo(model.SampleValue(42))
				require.Equal(t, labels["source"], model.LabelValue("mtls"))

				_, _, err = tests.RetryVectorScan(ctx, t, namespace, prom, "mtls_rejected_0", 1)
				require.Error(t, err)

				By("Deploying full VMCluster with every component protected by mTLS")
				install.DeleteVMAgent(t, kubeOpts, "vmagent-no-client-cert")
				install.DeleteVMAgent(t, kubeOpts, "vmagent")
				install.DeleteVMCluster(t, kubeOpts, consts.DefaultVMClusterName)
				install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, enterprisePatches(licensePatch, fullMTLSClusterPatch()))

				By("Verifying VMSelect accepts queries only with client certificate")
				installMTLSCurlPod(t, kubeOpts)
				_, err = runVMSelectQueryFromCurlPod(t, kubeOpts, namespace, false)
				require.Error(t, err)
				out, err := runVMSelectQueryFromCurlPod(t, kubeOpts, namespace, true)
				require.NoError(t, err)
				require.Contains(t, out, `"status":"success"`)
			})
	})

})

func enterpriseLicensePatch(kubeOpts *k8s.KubectlOptions) jsonpatch.Patch {
	if consts.LicenseFile() == "" {
		return nil
	}
	secretYaml, err := consts.PrepareLicenseSecret(namespace)
	require.NoError(t, err)
	k8s.KubectlApplyFromString(t, kubeOpts, secretYaml)
	patch, err := jsonpatch.DecodePatch([]byte(fmt.Sprintf(
		`[{"op":"add","path":"/spec/license","value":{"keyRef":{"name":%q,"key":%q}}}]`,
		consts.LicenseSecretName, consts.LicenseSecretKey,
	)))
	require.NoError(t, err)
	return patch
}

func enterprisePatches(licensePatch jsonpatch.Patch, patches ...jsonpatch.Patch) []jsonpatch.Patch {
	if len(licensePatch) == 0 {
		return patches
	}
	return append(patches, licensePatch)
}

func fullMTLSClusterPatch() jsonpatch.Patch {
	secretPath := "/etc/vm/secrets/" + mtlsSecretName
	componentArgs := map[string]string{
		"tls":                 "true",
		"tlsCertFile":         secretPath + "/server.crt",
		"tlsKeyFile":          secretPath + "/server.key",
		"mtls":                "true",
		"mtlsCAFile":          secretPath + "/ca.crt",
		"cluster.tls":         "true",
		"cluster.tlsCertFile": secretPath + "/server.crt",
		"cluster.tlsKeyFile":  secretPath + "/server.key",
		"cluster.tlsCAFile":   secretPath + "/ca.crt",
	}
	return tests.NewJSONPatchBuilder().
		Add("/spec/vminsert/secrets", []string{mtlsSecretName}).
		Add("/spec/vminsert/extraArgs", componentArgs).
		Add("/spec/vmselect/secrets", []string{mtlsSecretName}).
		Add("/spec/vmselect/extraArgs", componentArgs).
		Add("/spec/vmstorage/secrets", []string{mtlsSecretName}).
		Add("/spec/vmstorage/extraArgs", componentArgs).
		MustBuild()
}

func installMTLSCurlPod(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) {
	install.KubectlApplyFromString(t, kubeOpts, fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: vmselect-mtls-client
spec:
  restartPolicy: Never
  containers:
  - name: curl
    image: curlimages/curl:8.8.0
    command: ["sleep", "3600"]
    volumeMounts:
    - name: mtls
      mountPath: /mtls
      readOnly: true
  volumes:
  - name: mtls
    secret:
      secretName: %s
`, mtlsSecretName))
	k8s.RunKubectl(t, kubeOpts, "wait", "--for=condition=Ready", "pod/vmselect-mtls-client", "--timeout=600s")
}

func runVMSelectQueryFromCurlPod(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, withClientCert bool) (string, error) {
	args := []string{
		"exec", "pod/vmselect-mtls-client", "-c", "curl", "--",
		"curl", "--fail", "--silent", "--show-error",
		"--cacert", "/mtls/ca.crt",
	}
	if withClientCert {
		args = append(args, "--cert", "/mtls/client.crt", "--key", "/mtls/client.key")
	}
	args = append(args,
		"--data-urlencode", "query=1",
		fmt.Sprintf("https://%s/select/0/prometheus/api/v1/query", consts.GetVMSelectSvc(consts.DefaultVMClusterName, namespace)),
	)
	return k8s.RunKubectlAndGetOutputE(t, kubeOpts, args...)
}

func newMTLSCerts(namespace string) (mtlsCerts, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return mtlsCerts{}, err
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "vm-mtls-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(cryptorand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return mtlsCerts{}, err
	}

	serverCert, serverKey, err := newSignedCert(ca, caKey, "vmcluster", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, []string{
		fmt.Sprintf("vminsert-%s.%s.svc.cluster.local", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vminsert-%s.%s.svc", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vminsert-%s.%s", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vmselect-%s.%s.svc.cluster.local", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vmselect-%s.%s.svc", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vmselect-%s.%s", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vmstorage-%s.%s.svc.cluster.local", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vmstorage-%s.%s.svc", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("vmstorage-%s.%s", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("*.vmstorage-%s.%s.svc.cluster.local", consts.DefaultVMClusterName, namespace),
		fmt.Sprintf("*.vmstorage-%s.%s.svc", consts.DefaultVMClusterName, namespace),
	}, nil)
	if err != nil {
		return mtlsCerts{}, err
	}
	clientCert, clientKey, err := newSignedCert(ca, caKey, "vmagent", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, nil)
	if err != nil {
		return mtlsCerts{}, err
	}

	return mtlsCerts{
		caCert:     encodeCert(caDER),
		serverCert: serverCert,
		serverKey:  serverKey,
		clientCert: clientCert,
		clientKey:  clientKey,
	}, nil
}

func newSignedCert(ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, usages []x509.ExtKeyUsage, dnsNames []string, ips []net.IP) (string, string, error) {
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
		IPAddresses:  ips,
	}
	certDER, err := x509.CreateCertificate(cryptorand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return encodeCert(certDER), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), nil
}

func encodeCert(certDER []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
}
