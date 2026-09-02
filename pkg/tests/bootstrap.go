package tests

import (
	"context"
	"fmt"
	"sync"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2" //nolint:stylecheck,staticcheck

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
)

// InstallVMStackAndGather installs vmgather and the VictoriaMetrics k8s-stack Helm chart
// (which also installs VictoriaLogs) in parallel. Both require the ingress host to
// already be discovered by the caller. This stage is identical across every test suite's
// SynchronizedBeforeSuite bootstrap.
func InstallVMStackAndGather(ctx context.Context, t terratesting.TestingT) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		install.InstallVMGather(ctx, t)
	}()
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		install.InstallVMK8StackWithHelm(ctx, consts.VMK8sStackChart(), consts.SmokeValuesFile(), t, consts.DefaultVMNamespace, consts.DefaultReleaseName)
		install.InstallVictoriaLogs(ctx, t, consts.DefaultVMNamespace, consts.DefaultVLReleaseName, consts.DefaultVLCollectorReleaseName)
	}()
	wg.Wait()
}

// CleanupStaleNamespaces deletes any namespace left over from a previous, aborted run and
// carrying the given label (e.g. "vm-chaos-test=true"). Several suites run this before
// installing overwatch so aborted runs don't leak namespaces/resources into the next run.
func CleanupStaleNamespaces(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, label string) {
	k8s.RunKubectlContext(t, ctx, kubeOpts, "delete", "namespace", "-l", label,
		"--ignore-not-found=true", "--wait=true", fmt.Sprintf("--timeout=%s", consts.PollingTimeout))
}

// OverwatchStageOptions controls which optional tasks InstallOverwatchStage runs alongside
// installing overwatch itself.
type OverwatchStageOptions struct {
	// DeleteVMCluster removes the stock helm-managed VMCluster so tests can install their own.
	DeleteVMCluster bool
	// AddCustomAlertRules loads the suite's custom Alertmanager rules into the monitoring namespace.
	AddCustomAlertRules bool
}

// InstallOverwatchStage installs overwatch and, depending on opts, deletes the stock
// VMCluster and/or adds custom alert rules, all in parallel. This is the final bootstrap
// stage shared (with varying options) by every test suite.
func InstallOverwatchStage(ctx context.Context, t terratesting.TestingT, opts OverwatchStageOptions) {
	tasks := []func(){
		func() {
			install.InstallOverwatch(ctx, t, consts.OverwatchNamespace, consts.DefaultVMNamespace, consts.DefaultReleaseName)
		},
	}
	if opts.DeleteVMCluster {
		kubeOpts := k8s.NewKubectlOptions("", "", consts.DefaultVMNamespace)
		tasks = append(tasks, func() {
			install.DeleteVMCluster(t, kubeOpts, consts.DefaultReleaseName)
		})
	}
	if opts.AddCustomAlertRules {
		tasks = append(tasks, func() {
			install.AddCustomAlertRules(ctx, t, consts.DefaultVMNamespace)
		})
	}

	var wg sync.WaitGroup
	wg.Add(len(tasks))
	for _, task := range tasks {
		go func(fn func()) {
			defer GinkgoRecover()
			defer wg.Done()
			fn()
		}(task)
	}
	wg.Wait()
}
