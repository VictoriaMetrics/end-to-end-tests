package install

import (
	"context"
	"fmt"
	"os"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2" //nolint
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

const (
	strimziHelmRepo     = "strimzi"
	strimziHelmRepoURL  = "https://strimzi.io/charts/"
	strimziHelmChart    = "strimzi/strimzi-kafka-operator"
	strimziReleaseName  = "strimzi-kafka-operator"
	strimziOperatorName = "strimzi-cluster-operator"
)

// InstallStrimziOperator installs the Strimzi Kafka operator via Helm.
// The operator is configured to watch all namespaces so that Kafka CRs
// can be deployed in arbitrary test namespaces.
//
// Parameters:
// - ctx: context for the operation.
// - t: terratest testing interface.
// - namespace: namespace to install the operator into (typically consts.KafkaNamespace).
func InstallStrimziOperator(ctx context.Context, t terratesting.TestingT, namespace string) {
	By("Adding Strimzi Helm repo")
	_, err := helm.RunHelmCommandAndGetOutputE(t, &helm.Options{}, "repo", "add", strimziHelmRepo, strimziHelmRepoURL, "--force-update")
	require.NoError(t, err, "failed to add strimzi helm repo")

	_, err = helm.RunHelmCommandAndGetOutputE(t, &helm.Options{}, "repo", "update")
	require.NoError(t, err, "failed to update helm repos")

	kubeOpts := k8s.NewKubectlOptions("", "", namespace)

	By(fmt.Sprintf("Installing Strimzi operator in namespace %s", namespace))
	helmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		SetValues: map[string]string{
			"watchAnyNamespace": "true",
		},
		ExtraArgs: map[string][]string{
			"upgrade": {"--create-namespace", "--wait", "--timeout", "5m"},
		},
	}

	err = helm.UpgradeE(t, helmOpts, strimziHelmChart, strimziReleaseName)
	require.NoError(t, err, "failed to install strimzi-kafka-operator")

	By("Waiting for Strimzi CRDs to be established")
	clusterOpts := k8s.NewKubectlOptions("", "", "")
	for _, crd := range []string{
		"kafkanodepools.kafka.strimzi.io",
		"kafkas.kafka.strimzi.io",
		"kafkatopics.kafka.strimzi.io",
	} {
		require.Eventually(t, func() bool {
			_, err := k8s.RunKubectlAndGetOutputE(t, clusterOpts,
				"wait", "--for=condition=Established", "crd/"+crd, "--timeout=10s")
			return err == nil
		}, consts.ResourceWaitTimeout, consts.PollingInterval, "CRD %s not established", crd)
	}

	k8s.WaitUntilDeploymentAvailable(t, kubeOpts, strimziOperatorName, consts.Retries, consts.PollingInterval)
	helpers.Logf("Strimzi operator ready in namespace %s", namespace)
}

// InstallKafka deploys a single-node KRaft Kafka cluster (via Strimzi CRs) into the
// specified namespace, plus a KafkaTopic named "metrics".
//
// Prerequisites: InstallStrimziOperator must have been called first.
//
// Parameters:
// - ctx: context for the operation.
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options pointing at the target namespace.
// - namespace: namespace to deploy Kafka into.
func InstallKafka(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	helpers.Logf("Installing Kafka in namespace %s", namespace)

	for _, manifestFile := range []string{"kafka-node-pool.yaml", "kafka.yaml", "kafka-topic.yaml"} {
		manifestPath := consts.ManifestsRoot() + "/kafka/" + manifestFile
		rawYAML, err := os.ReadFile(manifestPath)
		require.NoError(t, err, "failed to read %s", manifestFile)

		docJSON, err := yaml.YAMLToJSON(rawYAML)
		require.NoError(t, err, "failed to convert %s to JSON", manifestFile)

		// Patch namespace into metadata
		namespacePatch, err := jsonpatch.DecodePatch([]byte(
			fmt.Sprintf(`[{"op":"add","path":"/metadata/namespace","value":%q}]`, namespace),
		))
		require.NoError(t, err)

		docJSON, err = namespacePatch.Apply(docJSON)
		require.NoError(t, err, "failed to patch namespace into %s", manifestFile)

		KubectlApplyFromString(t, kubeOpts, string(docJSON))
	}

	By("Waiting for Kafka cluster to be ready")
	require.Eventually(t, func() bool {
		_, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts,
			"wait", "--for=condition=Ready", "kafka/kafka", "--timeout=60s")
		return err == nil
	}, consts.ResourceWaitTimeout, consts.PollingInterval)

	helpers.Logf("Kafka ready in namespace %s", namespace)
}

// KafkaBrokerAddr returns the in-cluster bootstrap address for the Kafka cluster
// installed in the given namespace by InstallKafka.
func KafkaBrokerAddr(namespace string) string {
	return fmt.Sprintf("kafka-kafka-bootstrap.%s.svc.cluster.local:9092", namespace)
}

// DeleteKafka removes all Strimzi Kafka resources from the given namespace.
// Ignores not-found errors so it is safe to call in AfterEach.
func DeleteKafka(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) {
	helpers.Logf("Deleting Kafka resources")
	k8s.RunKubectl(t, kubeOpts, "delete", "kafka", "--all", "--ignore-not-found=true")
	k8s.RunKubectl(t, kubeOpts, "delete", "kafkanodepool", "--all", "--ignore-not-found=true")
	k8s.RunKubectl(t, kubeOpts, "delete", "kafkatopic", "--all", "--ignore-not-found=true")
}
