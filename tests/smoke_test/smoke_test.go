package smoke_test

import (
	"context"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/gather"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

func TestSmokeTests(t *testing.T) {
	tests.Init()
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Smoke test Suite", suiteConfig, reporterConfig)
}

var _ = Describe("Smoke test", Ordered, ContinueOnFailure, Label("smoke"), func() {
	ctx := context.Background()
	t := tests.GetT()
	var overwatch promquery.PrometheusClient

	BeforeAll(func() {
		install.DiscoverIngressHost(ctx, t)
		install.InstallVMGather(t)
		install.InstallVMK8StackWithHelm(
			ctx,
			consts.VMK8sStackChart,
			consts.SmokeValuesFile(),
			t,
			consts.DefaultVMNamespace,
			consts.DefaultReleaseName,
		)
		install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)

		var err error
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)
	})

	AfterEach(func() {
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		gather.K8sAfterAll(ctx, t, kubeOpts, consts.ResourceWaitTimeout)
		gather.VMAfterAll(ctx, t, consts.ResourceWaitTimeout, consts.DefaultReleaseName)
	})

	Describe("Inner", func() {
		It("Default installation should handle select requests for 5 mins", Label("kind", "id=37076a52-94ca-4de1-b1c8-029f8ce56bb7"), func() {
			By("Send requests for 5 minutes")
			tickerPeriod := time.Second

			vmSelectURL := tests.TenantSelectURL(consts.DefaultVMNamespace, 0)
			logger.Default.Logf(t, "Sending requests to %s", vmSelectURL)

			promAPI, err := tests.NewPromClientWithURL(vmSelectURL, overwatch.Start)
			require.NoError(t, err)

			ticker := time.NewTicker(tickerPeriod)
			defer ticker.Stop()

			started := time.Now()
			for ; true; <-ticker.C {
				_, _, err := promAPI.Query(ctx, "up")
				require.NoError(t, err)

				now := <-ticker.C
				if now.Sub(started) > 5*time.Minute {
					break
				}
			}

			By("No alerts are firing")
			overwatch.CheckNoAlertsFiring(ctx, t, consts.DefaultVMNamespace, nil)

			// By("At least 1k requests were made")
			// _, value, err := overwatch.VectorScan(ctx, "sum_over_time(sum(vm_http_requests_total)[30m])")
			// require.NoError(t, err)
			// require.GreaterOrEqual(t, value, float64(1_000))
		})
	})
})
