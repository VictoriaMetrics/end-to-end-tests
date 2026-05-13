package enterprise_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

var _ = Describe("VMAgent Kafka ingestion", func() {
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
					"wget", "-qO-", "http://localhost:8429/metrics")
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
