package install

import (
	"context"
	"fmt"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

const (
	dexImage     = "ghcr.io/dexidp/dex:v2.46.1"
	dexName      = "dex"
	dexConfigMap = "dex-config"
)

var dexPort int32 = 5556

// InstallDex deploys an ephemeral Dex OIDC provider into namespace as the test IdP for the
// vmauth SSO test. Returns Dex's issuer URL.
func InstallDex(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, vmauthHost string) string {
	issuer := fmt.Sprintf("http://%s", consts.DexHost(namespace))
	redirectURI := fmt.Sprintf("http://%s/_vmauth/sso/callback", vmauthHost)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: dexConfigMap, Namespace: namespace},
		Data:       map[string]string{"config.yaml": BuildDexConfig(issuer, redirectURI)},
	}
	applyObject(ctx, t, kubeOpts, cm)

	labels := map[string]string{"app": dexName}
	deployment := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: dexName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{1}[0],
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         dexName,
						Image:        dexImage,
						Command:      []string{"/usr/local/bin/dex", "serve", "/etc/dex/cfg/config.yaml"},
						Ports:        []corev1.ContainerPort{{Name: "http", ContainerPort: dexPort}},
						VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/dex/cfg"}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
							Path: "/.well-known/openid-configuration",
							Port: intstr.FromInt(int(dexPort)),
						}}},
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: dexConfigMap},
						}},
					}},
				},
			},
		},
	}
	applyObject(ctx, t, kubeOpts, deployment)

	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: dexName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Port: dexPort, TargetPort: intstr.FromInt(int(dexPort))}},
		},
	}
	applyObject(ctx, t, kubeOpts, svc)

	ingressYAML, err := helpers.BuildIngressManifest("dex-"+namespace, consts.DexHost(namespace), dexName, dexPort, false)
	require.NoError(t, err, "failed to build Dex ingress manifest")
	KubectlApplyFromString(ctx, t, kubeOpts, ingressYAML)

	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, dexName, consts.Retries, consts.PollingInterval)
	helpers.WaitForHTTPRoute(ctx, t, issuer+"/.well-known/openid-configuration")

	return issuer
}

func applyObject(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, obj interface{}) {
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	KubectlApplyFromString(ctx, t, kubeOpts, string(data))
}
