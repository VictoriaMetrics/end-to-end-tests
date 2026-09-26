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

// TransientWebhookFailure matches operator status reasons caused by a transient
// admission-webhook outage (e.g. GKE's built-in "warden-validating" webhook briefly
// refusing connections while RBAC resources are created). The operator retries
// reconciliation on its own, so callers should keep polling instead of failing.
const TransientWebhookFailure = "failed calling webhook"

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
			// Surface fetch errors instead of swallowing them: a persistent
			// List/Get failure (RBAC, webhook, API server) otherwise looks
			// identical to a resource silently stuck in "" status, and both
			// end up as an opaque "context deadline exceeded" at timeout.
			Logf("%s %s: failed to fetch status: %v - retrying", kind, namespace, err)
			return false, nil
		}
		if len(resources) == 0 {
			Logf("%s %s: no resources found yet - retrying", kind, namespace)
			return false, nil
		}
		for _, resource := range resources {
			switch resource.Status {
			case "":
				Logf("%s %s/%s: status not yet reported - retrying", kind, namespace, resource.Name)
				return false, nil
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
