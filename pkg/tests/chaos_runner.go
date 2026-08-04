package tests

import (
	"context"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

// PrepareChaosNamespace creates a fresh, isolated namespace for a chaos scenario: it
// removes any leftover namespace from a previous run, creates a new one, and labels it
// with staleLabel (e.g. "vm-chaos-test=true") so a future run's CleanupStaleNamespaces can
// find and remove it if this run is aborted before its own DeferCleanup runs.
func PrepareChaosNamespace(ctx context.Context, t terratesting.TestingT, namespace string, kubeOpts *k8s.KubectlOptions, staleLabel string) {
	CleanupNamespace(t, kubeOpts, namespace)
	EnsureNamespaceExists(t, kubeOpts, namespace)
	k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, staleLabel, "--overwrite")
}

// ClusterAffinityPatches builds the JSON patches that rename a cluster CR to clusterName
// and pin each of the given components to affinity. Used by chaos scenarios that install a
// fresh, isolated VMCluster/VLCluster per test.
func ClusterAffinityPatches(clusterName string, affinity map[string]interface{}, components []string) []jsonpatch.Patch {
	patches := []jsonpatch.Patch{}
	for _, component := range components {
		patches = append(patches, NewJSONPatchBuilder().
			Add("/metadata/name", clusterName).
			Add(fmt.Sprintf("/spec/%s/affinity", component), affinity).
			MustBuild())
	}
	return patches
}
