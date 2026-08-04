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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
			if pullErr := CheckForImagePullErrors(timeBoundContext, t, kubeOpts); pullErr != nil {
				require.NoError(t, pullErr)
				return
			}

			resources, err := fetch(timeBoundContext)
			if err != nil {
				continue
			}
			for _, resource := range resources {
				switch resource.Status {
				case vmv1beta1.UpdateStatusOperational:
					return
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
					return
				}
			}
		}
	}
}

var permanentImagePullReasons = map[string]bool{"InvalidImageName": true}

func IsPermanentImagePullReason(reason string) bool {
	return permanentImagePullReasons[reason]
}

func CheckForImagePullErrors(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) error {
	pods, err := k8s.ListPodsContextE(t, ctx, kubeOpts, metav1.ListOptions{LabelSelector: "managed-by=vm-operator"})
	if err != nil {
		return nil
	}
	for i := range pods {
		pod := &pods[i]
		for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
			for _, status := range statuses {
				if status.State.Waiting == nil || !IsPermanentImagePullReason(status.State.Waiting.Reason) {
					continue
				}
				message := status.State.Waiting.Message
				if message == "" {
					message = status.State.Waiting.Reason
				}
				return fmt.Errorf("pod %s/%s container %s stuck in %s: %s", pod.Namespace, pod.Name, status.Name, status.State.Waiting.Reason, message)
			}
		}
	}
	return nil
}
