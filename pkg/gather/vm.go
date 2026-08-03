package gather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/exporter"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests/allure"
)

const vmGatherExportAttempts = 3
const vmGatherExportTimeout = 5 * time.Minute

var errVMGatherExportFailed = errors.New("vmgather export failed")

// VMAfterAll provides cleanup and data collection logic for VictoriaMetrics components.
// It calls vmgather /api/export/start, polls /api/export/status,
// calls /api/export/download endpoints, and adds the downloaded archive to the report.
func VMAfterAll(ctx context.Context, t testing.TestingT, startTime time.Time, resourceWaitTimeout time.Duration, namespaces ...string) {
	err := retryVMGatherExport(vmGatherExportAttempts, 10*time.Second, func() error {
		return vmAfterAll(ctx, t, startTime, max(resourceWaitTimeout, vmGatherExportTimeout), namespaces)
	})
	if err != nil {
		logger.Default.Logf(t, "vmexporter export failed after %d attempts: %v", vmGatherExportAttempts, err)
	}
}

func retryVMGatherExport(attempts int, delay time.Duration, export func() error) error {
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = export()
		if !errors.Is(err, errVMGatherExportFailed) {
			return err
		}
		if attempt < attempts {
			time.Sleep(delay)
		}
	}
	return err
}

func vmAfterAll(ctx context.Context, t testing.TestingT, startTime time.Time, resourceWaitTimeout time.Duration, namespaces []string) error {
	endTime := time.Now()
	if startTime.IsZero() {
		startTime = endTime.Add(-1 * time.Hour)
	}
	namespaces = gatherNamespaces(namespaces...)

	// nil for TenantID as per JSON specification
	var tenantID *int = nil

	reqBody := exporter.RequestBody{
		Connection: exporter.Connection{
			URL:                fmt.Sprintf("http://%s", consts.GetVMSingleSvc(consts.DefaultReleaseName, consts.DefaultVMNamespace)),
			APIBasePath:        "/prometheus",
			TenantID:           tenantID,
			IsMultitenant:      false,
			FullAPIURL:         fmt.Sprintf("http://%s", consts.GetVMSingleSvc(consts.DefaultReleaseName, consts.DefaultVMNamespace)),
			Auth:               exporter.Auth{Type: "none"},
			SkipTLSVerify:      false,
			DisableCompression: true,
		},
		TimeRange: exporter.TimeRange{
			Start: startTime,
			End:   endTime,
		},
		Components: []string{"operator", "vmagent", "vmalert", "vminsert", "vmselect", "vmstorage", "k6"},
		Jobs:       []string{},
		Namespaces: namespaces,
		Obfuscation: exporter.Obfuscation{
			Enabled:           false,
			ObfuscateInstance: false,
			ObfuscateJob:      false,
			PreserveStructure: true,
			CustomLabels:      []string{},
		},
		StagingDir:        "/tmp/staging",
		MetricStepSeconds: 30,
		Batching: exporter.Batching{
			Enabled:            true,
			Strategy:           "custom",
			CustomIntervalSecs: 300,
		},
	}

	marshaledBody, err := json.Marshal(reqBody)
	if err != nil {
		logger.Default.Logf(t, "failed to marshal request body: %v", err)
		return err
	}

	startURL := url.URL{
		Scheme: "http",
		Host:   consts.VMGatherHost(),
		Path:   "/api/export/start",
	}
	logger.Default.Logf(t, "Making request to %s", startURL.String())
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL.String(), bytes.NewBuffer(marshaledBody))
	if err != nil {
		logger.Default.Logf(t, "failed to create HTTP request for /api/export/start: %v", err)
		return err
	}
	startReq.Header.Set("Content-Type", "application/json")
	logger.Default.Logf(t, "vmexporter /api/export/start request body: %s", string(marshaledBody))

	res, err := http.DefaultClient.Do(startReq)
	if err != nil {
		logger.Default.Logf(t, "failed to perform HTTP request to /api/export/start: %v", err)
		return err
	}
	if res.StatusCode != http.StatusOK {
		logger.Default.Logf(t, "unexpected status code from /api/export/start: %d", res.StatusCode)
		return fmt.Errorf("unexpected status code from /api/export/start: %d", res.StatusCode)
	}

	var startExportResponse struct {
		JobID string `json:"job_id"`
	}
	err = json.NewDecoder(res.Body).Decode(&startExportResponse)
	if err != nil {
		logger.Default.Logf(t, "failed to decode response from /api/export/start: %v", err)
		return err
	}
	if startExportResponse.JobID == "" {
		logger.Default.Logf(t, "job_id should not be empty in /api/export/start response")
		return errors.New("job_id should not be empty in /api/export/start response")
	}
	err = res.Body.Close()
	if err != nil {
		logger.Default.Logf(t, "failed to close response body: %v", err)
	}

	logger.Default.Logf(t, "vmexporter job started with ID: %s", startExportResponse.JobID)

	statusURL := url.URL{
		Scheme: "http",
		Host:   consts.VMGatherHost(),
		Path:   "/api/export/status",
	}
	logger.Default.Logf(t, "Making request to %s", statusURL.String())
	q := statusURL.Query()
	q.Add("id", startExportResponse.JobID)
	statusURL.RawQuery = q.Encode()
	statusURLStr := statusURL.String()

	var archivePath string
	pollErr := wait.PollUntilContextTimeout(ctx, 5*time.Second, resourceWaitTimeout, true, func(pollCtx context.Context) (bool, error) {
		statusReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, statusURLStr, nil)
		if err != nil {
			logger.Default.Logf(t, "failed to create HTTP request for /api/export/status: %v", err)
			return false, nil
		}

		statusRes, err := http.DefaultClient.Do(statusReq)
		if err != nil {
			logger.Default.Logf(t, "failed to perform HTTP request to /api/export/status: %v", err)
			return false, nil
		}
		defer statusRes.Body.Close()
		if statusRes.StatusCode != http.StatusOK {
			logger.Default.Logf(t, "unexpected status code from /api/export/status: %d", statusRes.StatusCode)
			return false, nil
		}

		var statusResponse struct {
			State  string `json:"state"`
			Result struct {
				ArchivePath string `json:"archive_path"`
			} `json:"result"`
		}
		if err := json.NewDecoder(statusRes.Body).Decode(&statusResponse); err != nil {
			logger.Default.Logf(t, "failed to decode response from /api/export/status: %v", err)
			return false, nil
		}

		logger.Default.Logf(t, "vmexporter job %s status: %s", startExportResponse.JobID, statusResponse.State)

		switch statusResponse.State {
		case "completed":
			archivePath = statusResponse.Result.ArchivePath
			if archivePath == "" {
				return false, errors.New("archive_path should not be empty when state is complete")
			}
			return true, nil
		case "failed":
			logger.Default.Logf(t, "vmexporter job %s statusResponse: %#v", startExportResponse.JobID, statusResponse)
			return false, errVMGatherExportFailed
		default:
			return false, nil
		}
	})
	if pollErr != nil {
		if errors.Is(pollErr, errVMGatherExportFailed) {
			return errVMGatherExportFailed
		}
		logger.Default.Logf(t, "polling for export status did not complete: %v", pollErr)
		return pollErr
	}

	logger.Default.Logf(t, "vmexporter job %s completed, archive path: %s", startExportResponse.JobID, archivePath)

	downloadURL := url.URL{
		Scheme: "http",
		Host:   consts.VMGatherHost(),
		Path:   "/api/download",
	}
	q = downloadURL.Query()
	q.Add("path", archivePath)
	downloadURL.RawQuery = q.Encode()
	downloadURLStr := downloadURL.String()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURLStr, nil)
	if err != nil {
		logger.Default.Logf(t, "failed to create HTTP request for /api/download: %v", err)
		return err
	}

	res, err = http.DefaultClient.Do(req)
	if err != nil {
		logger.Default.Logf(t, "failed to perform HTTP request to /api/download: %v", err)
		return err
	}
	if res.StatusCode != http.StatusOK {
		logger.Default.Logf(t, "unexpected status code from /api/download: %d", res.StatusCode)
		return fmt.Errorf("unexpected status code from /api/download: %d", res.StatusCode)
	}

	var zipBuffer bytes.Buffer
	_, err = zipBuffer.ReadFrom(res.Body)
	if err != nil {
		logger.Default.Logf(t, "failed to read downloaded zip to buffer: %v", err)
		return err
	}
	err = res.Body.Close()
	if err != nil {
		logger.Default.Logf(t, "failed to close response body: %v", err)
	}

	logger.Default.Logf(t, "Downloaded vmexporter archive into buffer, size: %d bytes", zipBuffer.Len())

	allure.AddAttachment("vmexporter-report.zip", allure.MimeTypeZIP, zipBuffer.Bytes())
	return nil
}

func gatherNamespaces(namespaces ...string) []string {
	seen := map[string]struct{}{consts.DefaultVMNamespace: {}}
	result := []string{consts.DefaultVMNamespace}
	for _, namespace := range namespaces {
		if namespace == "" {
			continue
		}
		if _, ok := seen[namespace]; ok {
			continue
		}
		seen[namespace] = struct{}{}
		result = append(result, namespace)
	}
	return result
}

// RestartOverwatchInstance restarts the monitoring VMSingle instance by deleting its pod
// and waiting for it to become operational again, to test resilience/config reloads.
func RestartOverwatchInstance(ctx context.Context, t testing.TestingT, kubeOpts *k8s.KubectlOptions) {
	client, err := k8s.GetKubernetesClientFromOptionsContextE(t, ctx, kubeOpts)
	require.NoError(t, err, "failed to get Kubernetes client")

	pods := k8s.ListPodsContext(t, ctx, kubeOpts, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/name=vmsingle", consts.DefaultReleaseName),
	})
	require.NotEmpty(t, pods, "no monitoring VMSingle pods found")
	firstPod := pods[0]

	err = client.CoreV1().Pods(kubeOpts.Namespace).Delete(ctx, firstPod.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "failed to delete pod %s", firstPod.Name)

	vmclient := install.GetVMClient(t, kubeOpts)
	install.WaitForVMSingleToBeOperational(ctx, t, kubeOpts, kubeOpts.Namespace, vmclient, consts.PollingTimeout)
}
