package install

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
)

// resourceStatus captures the operational status of a single VictoriaMetrics custom
// resource instance, decoupled from its concrete Go type so waitForOperational can be
// shared across VMAgent/VMAlert/VMAuth/VMCluster/VMDistributed/VMSingle/VLCluster.
type resourceStatus struct {
	Name   string
	Status vmv1beta1.UpdateStatus
	Reason string
}

// waitForOperational polls fetch every consts.PollingInterval until one of the returned
// resources reaches UpdateStatusOperational, one enters UpdateStatusFailed with a reason
// not present in transientReasons, the timeout expires, or ctx is cancelled.
//
// Polling (rather than a raw watch) is used because the API server/proxy can silently
// close a long-lived watch connection before the resource becomes ready, which would
// otherwise surface as a spurious hang/failure with no useful error.
func waitForOperational(
	ctx context.Context,
	t terratesting.TestingT,
	kubeOpts *k8s.KubectlOptions,
	timeout time.Duration,
	kind, namespace string,
	fetch func(ctx context.Context) ([]resourceStatus, error),
	transientReasons ...string,
) {
	if ctx.Err() != nil {
		return
	}

	timeBoundContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(consts.PollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeBoundContext.Done():
			if ctx.Err() == nil {
				require.NoError(t, fmt.Errorf("timed out waiting for %s in namespace %s to become operational", kind, namespace))
			}
			return
		case <-ticker.C:
			if pullErr := checkForImagePullErrors(timeBoundContext, t, kubeOpts); pullErr != nil {
				require.NoError(t, pullErr)
				return
			}

			resources, err := fetch(timeBoundContext)
			if err != nil {
				continue
			}
			for _, r := range resources {
				switch r.Status {
				case vmv1beta1.UpdateStatusOperational:
					return
				case vmv1beta1.UpdateStatusFailed:
					reason := strings.TrimSpace(r.Reason)
					if reason == "" {
						reason = "unknown reason"
					}
					if slices.ContainsFunc(transientReasons, func(tr string) bool { return strings.Contains(reason, tr) }) {
						helpers.Logf("%s %s/%s transiently failed: %s — retrying", kind, namespace, r.Name, reason)
						continue
					}
					require.NoError(t, fmt.Errorf("%s %s/%s entered failed state: %s", kind, namespace, r.Name, reason))
					return
				}
			}
		}
	}
}
