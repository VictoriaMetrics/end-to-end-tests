package gather

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/exporter"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
)

// VMAfterAll provides cleanup and data collection logic for VictoriaMetrics components.
// It calls vmgather /api/export/start, polls /api/export/status,
// calls /api/export/download endpoints, and adds the downloaded archive to the report.
func VMAfterAll(ctx context.Context, t testing.TestingT, resourceWaitTimeout time.Duration, releaseName string) {
	// Set start and end times dynamically
	endTime := time.Now()
	startTime := endTime.Add(-1 * time.Hour)

	// nil for TenantID as per JSON specification
	var tenantID *int = nil

	jobs := []string{"vmagent-vmks", "vmalert-vmks"}
	jobs = append(jobs, fmt.Sprintf("vmselect-%s", releaseName))
	jobs = append(jobs, fmt.Sprintf("vmstorage-%s", releaseName))
	jobs = append(jobs, fmt.Sprintf("vminsert-%s", releaseName))

	reqBody := exporter.RequestBody{
		Connection: exporter.Connection{
			URL:           fmt.Sprintf("http://%s/prometheus", consts.GetVMSingleSvc("overwatch", "overwatch")),
			APIBasePath:   "/prometheus",
			TenantID:      tenantID,
			IsMultitenant: false,
			FullAPIURL:    fmt.Sprintf("http://%s/prometheus", consts.GetVMSingleSvc("overwatch", "overwatch")),
			Auth:          exporter.Auth{Type: "none"},
			SkipTLSVerify: false,
		},
		TimeRange: exporter.TimeRange{
			Start: startTime,
			End:   endTime,
		},
		Components: []string{"operator", "vmagent", "vmalert", "vminsert", "vmselect", "vmstorage"},
		Jobs:       jobs,
		Obfuscation: exporter.Obfuscation{
			Enabled:           false,
			ObfuscateInstance: false,
			ObfuscateJob:      false,
			PreserveStructure: true,
			CustomLabels:      []string{},
		},
		StagingDir:        "/tmp/staging",
		MetricStepSeconds: 0,
		Batching: exporter.Batching{
			Enabled:            true,
			Strategy:           "custom",
			CustomIntervalSecs: 60,
		},
	}

	marshaledBody, err := json.Marshal(reqBody)
	if err != nil {
		logger.Default.Logf(t, "failed to marshal request body: %v", err)
		return
	}

	// Call vmgather's /api/export/start endpoint
	startURL := url.URL{
		Scheme: "http",
		Host:   consts.VMGatherHost(),
		Path:   "/api/export/start",
	}
	logger.Default.Logf(t, "Making request to %s", startURL.String())
	startReq, err := http.NewRequest(http.MethodPost, startURL.String(), bytes.NewBuffer(marshaledBody))
	if err != nil {
		logger.Default.Logf(t, "failed to create HTTP request for /api/export/start: %v", err)
		return
	}
	startReq.Header.Set("Content-Type", "application/json")
	logger.Default.Logf(t, "vmexporter /api/export/start request body: %s", string(marshaledBody))

	res, err := http.DefaultClient.Do(startReq)
	if err != nil {
		logger.Default.Logf(t, "failed to perform HTTP request to /api/export/start: %v", err)
		return
	}
	if res.StatusCode != http.StatusOK {
		logger.Default.Logf(t, "unexpected status code from /api/export/start: %d", res.StatusCode)
		return
	}

	var startExportResponse struct {
		JobID string `json:"job_id"`
	}
	err = json.NewDecoder(res.Body).Decode(&startExportResponse)
	if err != nil {
		logger.Default.Logf(t, "failed to decode response from /api/export/start: %v", err)
		return
	}
	if startExportResponse.JobID == "" {
		logger.Default.Logf(t, "job_id should not be empty in /api/export/start response")
		return
	}
	err = res.Body.Close()
	if err != nil {
		logger.Default.Logf(t, "failed to close response body: %v", err)
	}

	logger.Default.Logf(t, "vmexporter job started with ID: %s", startExportResponse.JobID)

	// Poll for job status until complete
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
	pollCtx, pollCancel := context.WithTimeout(ctx, resourceWaitTimeout)
	defer pollCancel()

OuterLoop:
	for {
		select {
		case <-pollCtx.Done():
			// Exit loop if context is cancelled, check archivePath later
			break OuterLoop
		default:
			statusReq, err := http.NewRequest(http.MethodGet, statusURLStr, nil)
			if err != nil {
				logger.Default.Logf(t, "failed to create HTTP request for /api/export/status: %v", err)
				continue
			}

			statusRes, err := http.DefaultClient.Do(statusReq)
			if err != nil {
				logger.Default.Logf(t, "failed to perform HTTP request to /api/export/status: %v", err)
				continue
			}
			if statusRes.StatusCode != http.StatusOK {
				logger.Default.Logf(t, "unexpected status code from /api/export/status: %d", statusRes.StatusCode)
				continue
			}

			var statusResponse struct {
				State  string `json:"state"`
				Result struct {
					ArchivePath string `json:"archive_path"`
				} `json:"result"`
			}
			err = json.NewDecoder(statusRes.Body).Decode(&statusResponse)
			if err != nil {
				logger.Default.Logf(t, "failed to decode response from /api/export/status: %v", err)
				statusRes.Body.Close()
				continue
			}
			err = statusRes.Body.Close()
			if err != nil {
				logger.Default.Logf(t, "failed to close response body: %v", err)
			}

			logger.Default.Logf(t, "vmexporter job %s status: %s", startExportResponse.JobID, statusResponse.State)

			switch statusResponse.State {
			case "completed":
				archivePath = statusResponse.Result.ArchivePath
				if archivePath == "" {
					logger.Default.Logf(t, "archive_path should not be empty when state is complete")
					return
				}
				break OuterLoop
			case "failed":
				logger.Default.Logf(t, "vmexporter job %s statusResponse: %#v", startExportResponse.JobID, statusResponse)
				logger.Default.Logf(t, "vmexporter job %s failed", startExportResponse.JobID)
				return
			default:
				time.Sleep(5 * time.Second)
			}
		}
	}

	// Check if archivePath was obtained, if not, it means polling timed out
	if archivePath == "" {
		logger.Default.Logf(t, "polling for export status timed out without completion or failure")
		return
	}

	logger.Default.Logf(t, "vmexporter job %s completed, archive path: %s", startExportResponse.JobID, archivePath)

	// Call download endpoint with archive_path as query param
	downloadURL := url.URL{
		Scheme: "http",
		Host:   consts.VMGatherHost(),
		Path:   "/api/download",
	}
	q = downloadURL.Query()
	q.Add("path", archivePath)
	downloadURL.RawQuery = q.Encode()
	downloadURLStr := downloadURL.String()

	req, err := http.NewRequest(http.MethodGet, downloadURLStr, nil)
	if err != nil {
		logger.Default.Logf(t, "failed to create HTTP request for /api/download: %v", err)
		return
	}

	res, err = http.DefaultClient.Do(req)
	if err != nil {
		logger.Default.Logf(t, "failed to perform HTTP request to /api/download: %v", err)
		return
	}
	if res.StatusCode != http.StatusOK {
		logger.Default.Logf(t, "unexpected status code from /api/download: %d", res.StatusCode)
		return
	}

	// Store the downloaded zip file content in a buffer
	var zipBuffer bytes.Buffer
	_, err = zipBuffer.ReadFrom(res.Body)
	if err != nil {
		logger.Default.Logf(t, "failed to read downloaded zip to buffer: %v", err)
		return
	}
	err = res.Body.Close()
	if err != nil {
		logger.Default.Logf(t, "failed to close response body: %v", err)
	}

	logger.Default.Logf(t, "Downloaded vmexporter archive into buffer, size: %d bytes", zipBuffer.Len())

	// Add the downloaded zip file content to the report
	ginkgo.AddReportEntry("vmexporter-report.zip", zipBuffer.String(), ginkgo.ReportEntryVisibilityNever)
}

// RestartOverwatchInstance restarts the overwatch VMSingle instance by deleting its pod
// and waiting for it to become operational again.
//
// This is used to test resilience or configuration reloads.
//
// Parameters:
// - ctx: context for the operation.
// - t: terratest testing interface.
// - namespace: Kubernetes namespace where overwatch is installed.
func RestartOverwatchInstance(ctx context.Context, t testing.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	client, err := k8s.GetKubernetesClientFromOptionsE(t, kubeOpts)
	require.NoError(t, err, "failed to get Kubernetes client")

	pods := k8s.ListPods(t, kubeOpts, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=overwatch",
	})
	require.NotEmpty(t, pods, "no overwatch pods found")
	firstPod := pods[0]

	// Delete the overwatch pod to trigger a restart
	err = client.CoreV1().Pods(namespace).Delete(ctx, firstPod.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "failed to delete pod %s", firstPod.Name)

	// Wait for overwatch VMSingle to become operational
	vmclient := install.GetVMClient(t, kubeOpts)
	install.WaitForVMSingleToBeOperational(ctx, t, kubeOpts, namespace, vmclient)
}
