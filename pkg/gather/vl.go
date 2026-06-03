package gather

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/testing"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests/allure"
)

// VLAfterAll fetches pod logs from VictoriaLogs in JSONL format via the ingress HTTP query API,
// archives them as a tar.gz, and attaches the archive to the Allure report.
func VLAfterAll(ctx context.Context, t testing.TestingT, startTime time.Time, resourceWaitTimeout time.Duration) {
	timeBoundCtx, cancel := context.WithTimeout(ctx, resourceWaitTimeout)
	defer cancel()

	endTime := time.Now()
	if startTime.IsZero() {
		startTime = endTime.Add(-1 * time.Hour)
	}

	queryURL := url.URL{
		Scheme: "http",
		Host:   consts.VLHost(),
		Path:   "/select/logsql/query",
	}
	q := queryURL.Query()
	q.Set("query", "*")
	q.Set("start", startTime.UTC().Format(time.RFC3339))
	q.Set("end", endTime.UTC().Format(time.RFC3339))
	queryURL.RawQuery = q.Encode()

	logger.Default.Logf(t, "VLAfterAll: querying VictoriaLogs at %s", queryURL.String())

	req, err := http.NewRequestWithContext(timeBoundCtx, http.MethodGet, queryURL.String(), nil)
	if err != nil {
		logger.Default.Logf(t, "VLAfterAll: failed to create request: %v", err)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Default.Logf(t, "VLAfterAll: query failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Default.Logf(t, "VLAfterAll: unexpected status %d from VictoriaLogs query", resp.StatusCode)
		return
	}

	jsonlData, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Default.Logf(t, "VLAfterAll: failed to read response body: %v", err)
		return
	}

	logger.Default.Logf(t, "VLAfterAll: fetched %d bytes of logs from VictoriaLogs", len(jsonlData))

	archive, err := buildTarGz("victoria-logs.jsonl", jsonlData)
	if err != nil {
		logger.Default.Logf(t, "VLAfterAll: failed to build archive: %v", err)
		return
	}

	allure.AddAttachment("victoria-logs.tar.gz", allure.MimeTypeGZIP, archive)
	logger.Default.Logf(t, "VLAfterAll: attached victoria-logs.tar.gz (%d bytes) to report", len(archive))
}

// buildTarGz creates an in-memory tar.gz archive containing a single file with the given content.
func buildTarGz(filename string, content []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:    filename,
		Mode:    0600,
		Size:    int64(len(content)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, fmt.Errorf("write tar header: %w", err)
	}
	if _, err := tw.Write(content); err != nil {
		return nil, fmt.Errorf("write tar content: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar writer: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}
	return buf.Bytes(), nil
}
