package install

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

var operatorReleaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)

func operatorCRDsURL(tag string) (string, error) {
	if !operatorReleaseTag.MatchString(tag) {
		return "", fmt.Errorf("operator image tag %q is not a release tag; set -operator-tag to a published operator version to install matching CRDs", tag)
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/VictoriaMetrics/operator/%s/config/crd/overlay/crd.yaml", tag), nil
}

func operatorCRDNames(output string) []string {
	var names []string
	for _, resource := range strings.Fields(output) {
		if _, name, ok := strings.Cut(resource, "/"); ok {
			names = append(names, name)
		}
	}
	return names
}

// InstallOperatorCRDs applies the CRD bundle for the exact operator image tag before Helm starts the operator.
func InstallOperatorCRDs(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, tag string) {
	crdsURL, err := operatorCRDsURL(tag)
	if err != nil {
		t.Fatalf("cannot install operator CRDs: %v", err)
	}
	// CRDs can exceed the client-side apply annotation limit; server-side apply also updates existing schemas.
	output, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "apply", "--server-side", "--force-conflicts", "-f", crdsURL, "-o", "name")
	if err != nil {
		t.Fatalf("cannot apply operator CRDs from %s: %v", crdsURL, err)
	}
	crdNames := operatorCRDNames(output)
	if len(crdNames) == 0 {
		t.Fatalf("no operator CRDs returned from %s", crdsURL)
	}
	WaitForCRDsEstablished(ctx, t, kubeOpts, crdNames...)
}
