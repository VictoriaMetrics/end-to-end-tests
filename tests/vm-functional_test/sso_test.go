package functional_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/require"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/install"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests"
)

// setupSSOFixtures installs Dex, SSO-protected VMAuth and a VMSingle backend, returning
// vmauth's external host.
func setupSSOFixtures(ctx context.Context, kubeOpts *k8s.KubectlOptions) string {
	vmauthHost := consts.VMAuthHost(namespace)

	dexIssuer := install.InstallDex(ctx, t, kubeOpts, namespace, vmauthHost)

	configYAML := install.BuildVMAuthSSOConfig(vmauthHost, dexIssuer, install.VMSingleBackendURL(namespace))
	install.ApplySSOConfigSecret(ctx, t, kubeOpts, namespace, configYAML)

	vmclient := install.GetVMClient(t, kubeOpts)
	patches := []jsonpatch.Patch{
		tests.NewJSONPatchBuilder().Add("/spec/ingress", map[string]interface{}{"host": vmauthHost}).MustBuild(),
		tests.NewJSONPatchBuilder().Add("/spec/externalConfig", map[string]interface{}{
			"secretRef": map[string]interface{}{"name": install.SSOConfigSecretName, "key": install.SSOConfigSecretKey},
		}).MustBuild(),
	}
	install.InstallVMAuth(ctx, t, kubeOpts, namespace, vmclient, patches)

	install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, nil, consts.PollingTimeout)

	return vmauthHost
}

var _ = Describe("VMAuth SSO test", Label("sso"), func() {
	var testStart time.Time

	BeforeEach(func(ctx context.Context) {
		testStart = time.Now()
		var err error
		namespace = tests.RandomNamespace("vm-sso")
		overwatch, err = tests.SetupOverwatchClient(ctx, t)
		require.NoError(t, err)
	})

	AfterEach(func(ctx context.Context) {
		kubeOpts := k8s.NewKubectlOptions("", "", namespace)
		tests.GatherOnFailureFrom(ctx, t, kubeOpts, namespace, testStart)
		install.DeleteVMAuth(t, kubeOpts, "vmauth")
		tests.CleanupNamespace(t, kubeOpts, namespace)
	})

	It("authenticates via Dex OIDC and proxies the request to the backend", Label("id=sso-login"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
		if !install.VMAuthSupportsSSO(consts.VMAuthDefaultVersion()) {
			Skip("vmauth version does not support the sso: config section (requires >= v1.153.0)")
		}
		kubeOpts := nsOpts()
		vmauthHost := setupSSOFixtures(ctx, kubeOpts)

		jar, err := cookiejar.New(nil)
		require.NoError(t, err)
		client := &http.Client{Jar: jar}

		protectedURL := fmt.Sprintf("http://%s/prometheus/api/v1/query?query=1", vmauthHost)

		loginPage, err := client.Get(protectedURL)
		require.NoError(t, err)
		defer loginPage.Body.Close()
		Expect(loginPage.StatusCode).To(Equal(http.StatusOK), "expected to land on Dex's login page")

		loginURL := loginPage.Request.URL.String()
		form := url.Values{"login": {install.SSOUsername}, "password": {install.SSOPassword}}
		resp, err := client.PostForm(loginURL, form)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		Expect(resp.StatusCode).To(Equal(http.StatusOK), "expected final backend response after SSO callback, got %s: %s", resp.Status, body)
		Expect(string(body)).To(ContainSubstring(`"status":"success"`))

		ssoCookie := false
		for _, cookie := range jar.Cookies(&url.URL{Scheme: "http", Host: vmauthHost}) {
			if cookie.Name == "_vmauth_sso" {
				ssoCookie = true
			}
		}
		Expect(ssoCookie).To(BeTrue(), "expected _vmauth_sso session cookie after login")
	})

	It("rejects unauthenticated POST requests with 401 instead of a login redirect", Label("id=sso-post-unauthenticated"), SpecTimeout(consts.VMFunctionalSpecTimeout), func(ctx context.Context) {
		if !install.VMAuthSupportsSSO(consts.VMAuthDefaultVersion()) {
			Skip("vmauth version does not support the sso: config section (requires >= v1.153.0)")
		}
		kubeOpts := nsOpts()
		vmauthHost := setupSSOFixtures(ctx, kubeOpts)

		protectedURL := fmt.Sprintf("http://%s/prometheus/api/v1/query", vmauthHost)
		resp, err := http.Post(protectedURL, "application/x-www-form-urlencoded", strings.NewReader("query=1"))
		require.NoError(t, err)
		defer resp.Body.Close()

		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})
})
