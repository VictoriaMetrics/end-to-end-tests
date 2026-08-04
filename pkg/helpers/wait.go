package helpers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

type ResourceStatus struct {
	Name   string
	Status vmv1beta1.UpdateStatus
	Reason string
}

func WaitForOperational(
	ctx context.Context,
	t terratesting.TestingT,
	kubeOpts *k8s.KubectlOptions,
	timeout time.Duration,
	kind, namespace string,
	fetch func(ctx context.Context) ([]ResourceStatus, error),
	transientReasons ...string,
) {
	if ctx.Err() != nil {
		return
	}

	err := wait.PollUntilContextTimeout(ctx, consts.PollingInterval, timeout, false, func(pollCtx context.Context) (bool, error) {
		resources, err := fetch(pollCtx)
		if err != nil {
			return false, nil
		}
		for _, resource := range resources {
			switch resource.Status {
			case vmv1beta1.UpdateStatusOperational:
				return true, nil
			case vmv1beta1.UpdateStatusFailed:
				reason := strings.TrimSpace(resource.Reason)
				if reason == "" {
					reason = "unknown reason"
				}
				if slices.ContainsFunc(transientReasons, func(transient string) bool {
					return strings.Contains(reason, transient)
				}) {
					Logf("%s %s/%s transiently failed: %s - retrying", kind, namespace, resource.Name, reason)
					continue
				}
				require.NoError(t, fmt.Errorf("%s %s/%s entered failed state: %s", kind, namespace, resource.Name, reason))
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil && ctx.Err() == nil {
		require.NoError(t, fmt.Errorf("timed out waiting for %s in namespace %s to become operational: %w", kind, namespace, err))
	}
}
