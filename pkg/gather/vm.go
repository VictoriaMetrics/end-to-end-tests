package gather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

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
	backoff := wait.Backoff{
		Duration: 10 * time.Second,
		Factor:   2.0,
		Steps:    vmGatherExportAttempts,
	}
	err := retry.OnError(backoff, isRetriableVMGatherExportError, func() error {
		return vmAfterAll(ctx, t, startTime, max(resourceWaitTimeout, vmGatherExportTimeout), namespaces)
	})
	if err != nil {
		logger.Default.Logf(t, "vmexporter export failed after %d attempts: %v", vmGatherExportAttempts, err)
	}
}

func isRetriableVMGatherExportError(err error) bool {
	return errors.Is(err, errVMGatherExportFailed)
}

// doVMGatherRequest builds and executes an HTTP request against vmgather, logging any
// failure using step for context. On success it returns the response with a 200 OK
// status; callers are responsible for closing the response body.
func doVMGatherRequest(ctx context.Context, t testing.TestingT, method, targetURL string, body io.Reader, step string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		logger.Default.Logf(t, "failed to create HTTP request for %s: %v", step, err)
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Default.Logf(t, "failed to perform HTTP request to %s: %v", step, err)
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		logger.Default.Logf(t, "unexpected status code from %s: %d", step, res.StatusCode)
		res.Body.Close()
		return nil, fmt.Errorf("unexpected status code from %s: %d", step, res.StatusCode)
	}
	return res, nil
}

func vmAfterAll(ctx context.Context, t testing.TestingT, startTime time.Time, resourceWaitTimeout time.Duration, namespaces []string) error {
	endTime := time.Now()
	if startTime.IsZero() {
		startTime = endTime.Add(-1 * time.Hour)
	}
	namespaces = sets.List(gatherNamespaces(namespaces...))

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
	logger.Default.Logf(t, "vmexporter /api/export/start request body: %s", string(marshaledBody))
	res, err := doVMGatherRequest(ctx, t, http.MethodPost, startURL.String(), bytes.NewBuffer(marshaledBody), "/api/export/start")
	if err != nil {
		return err
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
		statusRes, err := doVMGatherRequest(pollCtx, t, http.MethodGet, statusURLStr, nil, "/api/export/status")
		if err != nil {
			return false, nil
		}
		defer statusRes.Body.Close()

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

	res, err = doVMGatherRequest(ctx, t, http.MethodGet, downloadURLStr, nil, "/api/download")
	if err != nil {
		return err
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

func gatherNamespaces(namespaces ...string) sets.Set[string] {
	result := sets.New(consts.DefaultVMNamespace)
	for _, namespace := range namespaces {
		if namespace == "" {
			continue
		}
		result.Insert(namespace)
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
