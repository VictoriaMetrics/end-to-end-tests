package install

import (
	"context"
	"fmt"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	SSOClientID     = "vmauth-sso-test"
	SSOClientSecret = "vmauth-sso-test-secret"
	SSOCookieSecret = "vmauth-sso-test-cookie-secret-32"
	SSOUsername     = "admin@example.com"
	SSOPassword     = "password"
	// ssoPasswordHash: dex's published example bcrypt hash of SSOPassword.
	ssoPasswordHash = "$2a$10$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W"
	ssoUserID       = "08a8684b-db88-4b73-90a9-3cd1661f5466"

	SSOConfigSecretName = "vmauth-sso-config"
	SSOConfigSecretKey  = "config.yaml"

	vmSingleBackendPort = 8428
)

// BuildDexConfig renders a Dex config.yaml: ephemeral sqlite store, one static client/user,
// consent screen skipped.
func BuildDexConfig(issuer, redirectURI string) string {
	return fmt.Sprintf(`issuer: %q
storage:
  type: sqlite3
  config:
    file: ":memory:"
web:
  http: 0.0.0.0:5556
enablePasswordDB: true
oauth2:
  skipApprovalScreen: true
staticClients:
- id: %q
  secret: %q
  name: "VMAuth SSO test client"
  redirectURIs:
  - %q
staticPasswords:
- email: %q
  hash: %q
  username: %q
  userID: %q
`, issuer, SSOClientID, SSOClientSecret, redirectURI, SSOUsername, ssoPasswordHash, SSOUsername, ssoUserID)
}

// BuildVMAuthSSOConfig renders a vmauth config.yaml with the "sso:" OIDC section and a
// matching "users" entry proxying to backendURLPrefix.
func BuildVMAuthSSOConfig(vmauthHost, dexIssuer, backendURLPrefix string) string {
	return fmt.Sprintf(`sso:
- src_host: %q
  oidc:
    issuer: %q
    client_id: %q
    client_secret: %q
    scopes: ["openid", "profile", "email"]
    cookie_secret: %q
    insecure: true
users:
- jwt:
    match_claims:
      iss: %q
      aud: %q
    oidc:
      issuer: %q
    # Dex tokens have no vm_access claim; without a default vmauth denies access.
    default_vm_access_claim:
      metrics_account_id: 0
  url_map:
  - src_paths: ["/.*"]
    src_hosts: [%q]
    url_prefix: %q
`, vmauthHost, dexIssuer, SSOClientID, SSOClientSecret, SSOCookieSecret, dexIssuer, SSOClientID, dexIssuer, vmauthHost, backendURLPrefix)
}

// VMSingleBackendURL returns the in-cluster URL of the VMSingle instance installed in namespace.
func VMSingleBackendURL(namespace string) string {
	return fmt.Sprintf("http://vmsingle-vmsingle.%s.svc.cluster.local:%d", namespace, vmSingleBackendPort)
}

// ApplySSOConfigSecret applies configYAML as the Secret referenced by VMAuth's
// spec.externalConfig.secretRef.
func ApplySSOConfigSecret(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, configYAML string) {
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: SSOConfigSecretName, Namespace: namespace},
		StringData: map[string]string{SSOConfigSecretKey: configYAML},
	}
	applyObject(ctx, t, kubeOpts, secret)
}
