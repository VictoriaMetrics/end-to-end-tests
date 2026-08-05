package tests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

func VLIngest(ctx context.Context, insertURL, streamField, streamValue string, lines []string) error {
	var buf bytes.Buffer
	for _, line := range lines {
		buf.WriteString(line + "\n")
	}
	u := fmt.Sprintf("%s/insert/jsonline?_stream_fields=%s", insertURL, url.QueryEscape(streamField))
	return VLPost(ctx, u, "application/stream+json", buf.Bytes(), "ingest request")
}

func VLPost(ctx context.Context, targetURL, contentType string, payload []byte, op string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build %s: %w", op, err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s returned %s: %s", op, resp.Status, string(body))
	}
	return nil
}

func VLQuery(ctx context.Context, selectURL, query string, start, end time.Time) ([]byte, error) {
	u, err := url.Parse(selectURL + "/select/logsql/query")
	if err != nil {
		return nil, fmt.Errorf("build query url: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("start", start.UTC().Format(time.RFC3339))
	q.Set("end", end.UTC().Format(time.RFC3339))
	u.RawQuery = q.Encode()
	return VLGet(ctx, u.String(), "query request")
}

func VLStatsCount(ctx context.Context, selectURL, query string) ([]byte, error) {
	u, err := url.Parse(selectURL + "/select/logsql/stats_query")
	if err != nil {
		return nil, fmt.Errorf("build stats url: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()
	return VLGet(ctx, u.String(), "stats request")
}

func VLGet(ctx context.Context, targetURL, op string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", op, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", op, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("%s returned %s: %s", op, resp.Status, string(body))
	}
	return body, nil
}

func InstallVLCurlPod(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, podName, secretName string) {
	clientset, err := k8s.GetKubernetesClientFromOptionsE(t, kubeOpts)
	if err != nil {
		t.Errorf("failed to create Kubernetes client: %v", err)
		return
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "curl", Image: "curlimages/curl:8.8.0", Command: []string{"sleep", "3600"}, VolumeMounts: []corev1.VolumeMount{{Name: "mtls", MountPath: "/mtls", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "mtls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName}}}}}}
	_, err = clientset.CoreV1().Pods(kubeOpts.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("failed to create mTLS curl pod: %v", err)
		return
	}
	if _, err = k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pod/"+podName, fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout)); err != nil {
		t.Errorf("mTLS curl pod did not become ready: %v", err)
	}
}

func RunVLCurl(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, podName, targetURL string, withClientCert bool, curlArgs ...string) (string, error) {
	args := []string{"exec", "pod/" + podName, "-c", "curl", "--", "curl", "--fail", "--silent", "--show-error", "--verbose", "--cacert", "/mtls/ca.crt"}
	if withClientCert {
		args = append(args, "--cert", "/mtls/client.crt", "--key", "/mtls/client.key")
	}
	args = append(args, curlArgs...)
	args = append(args, targetURL)
	out, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, args...)
	helpers.Logf("curl %s (clientCert=%t) error=%v\n%s", targetURL, withClientCert, err, out)
	return out, err
}
