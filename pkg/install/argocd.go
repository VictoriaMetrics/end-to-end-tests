package install

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/stretchr/testify/require"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

const (
	argocdNamespace           = "argocd"
	argocdInstallURLTemplate  = "https://raw.githubusercontent.com/argoproj/argo-cd/%s/manifests/install.yaml"
	argocdApplicationResource = "applications.argoproj.io"
	argocdRepoURL             = "https://victoriametrics.github.io/helm-charts"
	// argocdResourcesFinalizer makes Argo CD cascade-delete an Application's managed
	// resources when the Application object itself is deleted, instead of orphaning them.
	argocdResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"
)

type argocdApplication struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations,omitempty"`
		Finalizers  []string          `json:"finalizers,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Project string `json:"project"`
		Source  struct {
			RepoURL        string `json:"repoURL"`
			Chart          string `json:"chart"`
			TargetRevision string `json:"targetRevision"`
			Helm           struct {
				Parameters []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"parameters,omitempty"`
			} `json:"helm"`
		} `json:"source"`
		Destination struct {
			Server    string `json:"server"`
			Namespace string `json:"namespace"`
		} `json:"destination"`
		SyncPolicy struct {
			Automated struct {
				Prune    bool `json:"prune"`
				SelfHeal bool `json:"selfHeal"`
			} `json:"automated"`
			SyncOptions []string `json:"syncOptions"`
		} `json:"syncPolicy"`
	} `json:"spec"`
}

type argoCDParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type argoCDHelm struct {
	Parameters []argoCDParameter `json:"parameters"`
}

type argoCDSource struct {
	RepoURL        string     `json:"repoURL"`
	Chart          string     `json:"chart"`
	TargetRevision string     `json:"targetRevision"`
	Helm           argoCDHelm `json:"helm"`
}

type argoCDDestination struct {
	Server    string `json:"server"`
	Namespace string `json:"namespace"`
}

type argoCDApplicationState struct {
	Spec struct {
		Source      argoCDSource      `json:"source"`
		Destination argoCDDestination `json:"destination"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status     string `json:"status"`
			ComparedTo struct {
				Source      argoCDSource      `json:"source"`
				Destination argoCDDestination `json:"destination"`
			} `json:"comparedTo"`
		} `json:"sync"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
	} `json:"status"`
}

func (app argoCDApplicationState) current() bool {
	desired, compared := app.Spec.Source, app.Status.Sync.ComparedTo.Source
	if len(desired.Helm.Parameters) != len(compared.Helm.Parameters) {
		return false
	}
	parameters := make(map[string]string, len(desired.Helm.Parameters))
	for _, param := range desired.Helm.Parameters {
		parameters[param.Name] = param.Value
	}
	comparedParameters := make(map[string]string, len(compared.Helm.Parameters))
	for _, param := range compared.Helm.Parameters {
		comparedParameters[param.Name] = param.Value
	}
	return app.Status.Sync.Status == "Synced" && app.Status.Health.Status == "Healthy" &&
		desired.RepoURL == compared.RepoURL && desired.Chart == compared.Chart &&
		desired.TargetRevision == compared.TargetRevision && maps.Equal(parameters, comparedParameters) &&
		app.Spec.Destination == app.Status.Sync.ComparedTo.Destination
}

// InstallArgoCD installs Argo CD manifests at version and waits for its API server.
func InstallArgoCD(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, version string) {
	_, _ = k8s.RunKubectlAndGetOutputE(t, kubeOpts, "create", "namespace", argocdNamespace)
	argocdOpts := k8s.NewKubectlOptions(kubeOpts.ContextName, kubeOpts.ConfigPath, argocdNamespace)
	k8s.RunKubectlContext(t, ctx, argocdOpts, "apply", "--server-side", "--force-conflicts", "-f", fmt.Sprintf(argocdInstallURLTemplate, version))
	argoCDRetries := int(consts.ArgoCDWaitTimeout / consts.PollingInterval)
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, argocdOpts, "argocd-server", argoCDRetries, consts.PollingInterval)
}

// BuildArgoCDHelmApplication creates an Application for a chart in Helm repository.
func BuildArgoCDHelmApplication(name, namespace, chart, revision string, parameters map[string]string) (string, error) {
	return buildArgoCDHelmApplication(name, namespace, chart, revision, parameters, true)
}

// BuildArgoCDHelmApplicationWithoutSSA creates an Application without server-side apply.
func BuildArgoCDHelmApplicationWithoutSSA(name, namespace, chart, revision string, parameters map[string]string) (string, error) {
	return buildArgoCDHelmApplication(name, namespace, chart, revision, parameters, false)
}

func buildArgoCDHelmApplication(name, namespace, chart, revision string, parameters map[string]string, serverSideApply bool) (string, error) {
	app := argocdApplication{APIVersion: "argoproj.io/v1alpha1", Kind: "Application"}
	app.Metadata.Name = name
	app.Metadata.Namespace = argocdNamespace
	app.Metadata.Annotations = map[string]string{
		"argocd.argoproj.io/compare-options": "ServerSideDiff=false",
	}
	app.Metadata.Finalizers = []string{argocdResourcesFinalizer}
	app.Spec.Project = "default"
	app.Spec.Source.RepoURL = argocdRepoURL
	app.Spec.Source.Chart = chart
	app.Spec.Source.TargetRevision = revision
	app.Spec.Destination.Server = "https://kubernetes.default.svc"
	app.Spec.Destination.Namespace = namespace
	app.Spec.SyncPolicy.Automated.Prune = true
	app.Spec.SyncPolicy.Automated.SelfHeal = true
	app.Spec.SyncPolicy.SyncOptions = []string{"CreateNamespace=true"}
	if serverSideApply {
		app.Spec.SyncPolicy.SyncOptions = append(app.Spec.SyncPolicy.SyncOptions, "ServerSideApply=true")
	}
	for name, value := range parameters {
		app.Spec.Source.Helm.Parameters = append(app.Spec.Source.Helm.Parameters, struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: name, Value: value})
	}
	data, err := yaml.Marshal(app)
	if err != nil {
		return "", fmt.Errorf("marshal Argo CD Application: %w", err)
	}
	return string(data), nil
}

// ApplyArgoCDApplication applies Application manifest and waits for sync and health.
func ApplyArgoCDApplication(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifest, name string) {
	argocdOpts := k8s.NewKubectlOptions(kubeOpts.ContextName, kubeOpts.ConfigPath, argocdNamespace)
	KubectlApplyFromStringWithRetry(ctx, t, argocdOpts, manifest)
	require.Eventually(t, func() bool {
		output, err := k8s.RunKubectlAndGetOutputContextE(t, ctx, argocdOpts, "get", argocdApplicationResource, name, "-o", "json")
		if err != nil {
			return false
		}
		var app argoCDApplicationState
		return json.Unmarshal([]byte(output), &app) == nil && app.current()
	}, consts.ArgoAppWaitTimeout, consts.PollingInterval, "Argo CD Application %s did not sync the current source and become Healthy", name)
}

// DeleteArgoCDApplication removes Application and waits for its object to disappear.
func DeleteArgoCDApplication(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, name string) {
	if kubeOpts == nil {
		return
	}
	argocdOpts := k8s.NewKubectlOptions(kubeOpts.ContextName, kubeOpts.ConfigPath, argocdNamespace)
	k8s.RunKubectlContext(t, context.Background(), argocdOpts, "delete", argocdApplicationResource, name, "--ignore-not-found=true")
	k8s.RunKubectlContext(t, context.Background(), argocdOpts, "wait", "--for=delete", argocdApplicationResource, name, fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
}
