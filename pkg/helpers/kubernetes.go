package helpers

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
)

// GetDynamicClient creates a Kubernetes dynamic client from kubeconfig in kubeOpts.
func GetDynamicClient(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) *dynamic.DynamicClient {
	kubeConfigPath, err := kubeOpts.GetConfigPath(t)
	require.NoError(t, err)
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath}, &clientcmd.ConfigOverrides{})
	restConfig, err := clientConfig.ClientConfig()
	require.NoError(t, err)
	return dynamic.NewForConfigOrDie(restConfig)
}

// VMVersionFromCR reads VM version from VMSingle CR label.
func VMVersionFromCR(t terratesting.TestingT, ctx context.Context, kubeOpts *k8s.KubectlOptions, releaseName string) string {
	out, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "get", "vmsingle", releaseName, "-o", `jsonpath={.metadata.labels.app\.kubernetes\.io/version}`)
	if err != nil {
		Logf("WARNING: failed to get VMSingle %s: %v", releaseName, err)
		return ""
	}
	if out = strings.TrimSpace(out); out == "" {
		Logf("WARNING: VMSingle %s has no app.kubernetes.io/version annotation", releaseName)
	}
	return out
}

// SetVMOperatorEnv sets an env var on the installed VictoriaMetrics operator and waits for rollout.
func SetVMOperatorEnv(ctx context.Context, t terratesting.TestingT, namespace, name, value string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	deploymentName := fmt.Sprintf("%s-victoria-metrics-operator", consts.DefaultReleaseName)
	k8s.RunKubectlContext(t, ctx, kubeOpts, "set", "env", fmt.Sprintf("deployment/%s", deploymentName), fmt.Sprintf("%s=%s", name, value))
	k8s.RunKubectlContext(t, ctx, kubeOpts, "rollout", "status", fmt.Sprintf("deployment/%s", deploymentName), "--timeout=120s")
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, deploymentName, consts.Retries, consts.PollingInterval)
}

// WaitForHTTPRoute polls ready URLs until one responds with HTTP 200.
func WaitForHTTPRoute(ctx context.Context, t terratesting.TestingT, readyURL string) {
	WaitForHTTPRoutes(ctx, t, readyURL)
}

// WaitForHTTPRoutes polls ready URLs until one responds with HTTP 200 or the wait times out.
func WaitForHTTPRoutes(ctx context.Context, t terratesting.TestingT, readyURLs ...string) {
	client := &http.Client{
		Timeout:   consts.HTTPClientTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test readiness accepts self-signed certs
	}
	require.Eventually(t, func() bool {
		for _, readyURL := range readyURLs {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		return false
	}, consts.ResourceWaitTimeout, consts.PollingInterval, "ingress routes %s did not become ready", strings.Join(readyURLs, ", "))
}

// MergeEnvVars overrides base entries whose names exist in extra and appends new entries.
func MergeEnvVars(base []corev1.EnvVar, extra map[string]string) []corev1.EnvVar {
	for name, value := range extra {
		found := false
		for i := range base {
			if base[i].Name == name {
				base[i].Value = value
				found = true
				break
			}
		}
		if !found {
			base = append(base, corev1.EnvVar{Name: name, Value: value})
		}
	}
	return base
}

// DeleteAllSeriesBeforeScenario clears VMSelect data before load scenario execution.
func DeleteAllSeriesBeforeScenario(ctx context.Context, vmselectSvc string) {
	deleteSeriesURL := fmt.Sprintf("http://%s/delete_series?%s", vmselectSvc, url.Values{
		"match[]": []string{`{__name__!=""}`},
		"end":     []string{fmt.Sprintf("%d", time.Now().Unix())},
	}.Encode())
	client := &http.Client{Timeout: consts.HTTPClientTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deleteSeriesURL, nil)
	if err != nil {
		Logf("WARNING: failed to build delete_series request: %v", err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		Logf("WARNING: delete_series request failed: %v", err)
		return
	}
	defer resp.Body.Close()
	Logf("Deleted all series before k6 scenario start (status %d)", resp.StatusCode)
}

// BuildIngressManifest builds a Traefik ingress for a service.
//
// Note on https: unlike nginx-ingress (which had a single
// "nginx.ingress.kubernetes.io/backend-protocol" annotation to force HTTPS to
// the backend), Traefik's Kubernetes Ingress provider determines backend
// scheme from the referenced Service's port name ("https") or its
// appProtocol field, not from an annotation on the Ingress object. This
// function doesn't own that Service, so it cannot guarantee HTTPS backends
// work here — that depends on how serviceName's port is declared by its
// caller.
func BuildIngressManifest(name, host, serviceName string, servicePort int32, https bool) (string, error) {
	pathType := networkingv1.PathTypePrefix
	ingress := networkingv1.Ingress{
		TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ptr("traefik"),
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
					Path:     "/",
					PathType: &pathType,
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
						Name: serviceName,
						Port: networkingv1.ServiceBackendPort{Number: servicePort},
					}},
				}}}},
			}},
		},
	}
	data, err := yaml.Marshal(ingress)
	return string(data), err
}

func ptr[T any](value T) *T { return &value }
