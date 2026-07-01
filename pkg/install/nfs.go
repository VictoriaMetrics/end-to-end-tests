package install

import (
	"context"
	"fmt"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

const (
	nfsExportPath = "/nfsshare"
	// nfsVMStorageReplicas must match vmstorage replicaCount in vmcluster.yaml.
	nfsVMStorageReplicas = 2
	// nfsVMSelectReplicas must match vmselect replicaCount in vmcluster.yaml.
	nfsVMSelectReplicas = 2
	nfsStorageSize      = "10Gi"
	nfsCacheSize        = "2Gi"
)

func nfsVMStoragePVName(namespace string, idx int) string {
	return fmt.Sprintf("nfs-pv-%s-vmstorage-%d", namespace, idx)
}
func nfsVMSelectPVName(namespace string, idx int) string {
	return fmt.Sprintf("nfs-pv-%s-vmselect-%d", namespace, idx)
}
func nfsStorageClassName(namespace string) string {
	return fmt.Sprintf("nfs-static-%s", namespace)
}

const nfsServerPodName = "nfs-server"

// InstallNFSServer deploys a single NFS server pod and Service into the given namespace,
// creates a per-namespace local StorageClass backed by static NFS PersistentVolumes,
// and returns the StorageClass name to be patched into VMCluster's
// vmstorage.storage.volumeClaimTemplate.spec.storageClassName.
//
// The server exports /nfsshare with fsid=0, making it the NFSv4 pseudoroot. Each
// vmstorage replica gets its own subdirectory (/nfsshare/0, /nfsshare/1, …) created
// by an init container. PVs use NFSv4 paths relative to the pseudoroot (/0, /1, …)
// so each replica has isolated storage from a single server pod.
//
// NFSv4 is required: NFSv3 mountd only grants access to exact exported paths, so
// subdirectory mounts of /nfsshare would fail. With NFSv4 and fsid=0, the client
// mounts server:/<idx> which resolves to /nfsshare/<idx> on the server.
//
// emptyDir is mounted at /nfsshare so the path exists before exportfs runs — the image
// does NOT mkdir SHARED_DIRECTORY itself and exits with "Failed to stat" without it.
// The init container pre-creates per-replica subdirs in the same emptyDir.
func InstallNFSServer(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) string {
	helpers.Logf("Installing NFS server in namespace %s", namespace)

	clientset, err := k8s.GetKubernetesClientFromOptionsE(t, kubeOpts)
	require.NoError(t, err, "failed to get kubernetes client")

	storageQty := resource.MustParse(nfsStorageSize)
	scName := nfsStorageClassName(namespace)

	// Build mkdir command for all vmstorage and vmselect subdirectories.
	mkdirCmd := "mkdir -p"
	for i := range nfsVMStorageReplicas {
		mkdirCmd += fmt.Sprintf(" %s/vmstorage/%d", nfsExportPath, i)
	}
	for i := range nfsVMSelectReplicas {
		mkdirCmd += fmt.Sprintf(" %s/vmselect/%d", nfsExportPath, i)
	}

	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      nfsServerPodName,
			Namespace: namespace,
			Labels:    map[string]string{"app": nfsServerPodName},
		},
		Spec: corev1.PodSpec{
			// Init container pre-creates per-replica subdirs in the shared emptyDir.
			// kubectl exec cannot be used: GKE prevents setns into privileged containers.
			InitContainers: []corev1.Container{
				{
					Name:    "init-dirs",
					Image:   "busybox:latest",
					Command: []string{"sh", "-c", mkdirCmd},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "nfs-data", MountPath: nfsExportPath},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  nfsServerPodName,
					Image: "itsthenetwork/nfs-server-alpine:latest",
					Env: []corev1.EnvVar{
						{Name: "SHARED_DIRECTORY", Value: nfsExportPath},
					},
					SecurityContext: &corev1.SecurityContext{Privileged: boolPtr(true)},
					Ports: []corev1.ContainerPort{
						{Name: "nfs", ContainerPort: 2049, Protocol: corev1.ProtocolTCP},
					},
					// emptyDir provides /nfsshare so exportfs can stat the path.
					VolumeMounts: []corev1.VolumeMount{
						{Name: "nfs-data", MountPath: nfsExportPath},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name:         "nfs-data",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
			},
			NodeSelector: map[string]string{"monitoring": "true"},
			Tolerations: []corev1.Toleration{
				{Key: "monitoring", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}
	podYAML, err := yaml.Marshal(pod)
	require.NoError(t, err, "failed to marshal NFS server pod")
	KubectlApplyFromString(ctx, t, kubeOpts, string(podYAML))

	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{Kind: "Service", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      nfsServerPodName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": nfsServerPodName},
			Ports:    []corev1.ServicePort{{Name: "nfs", Port: 2049, Protocol: corev1.ProtocolTCP}},
		},
	}
	svcYAML, err := yaml.Marshal(svc)
	require.NoError(t, err, "failed to marshal NFS server service")
	KubectlApplyFromString(ctx, t, kubeOpts, string(svcYAML))

	// Wait for NFS pod to be Ready (init + main container).
	helpers.Logf("Waiting for NFS server pod to be ready in namespace %s", namespace)
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pod", nfsServerPodName,
		fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))

	// Resolve Service ClusterIP.
	var clusterIP string
	pollCtx, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	err = wait.PollUntilContextTimeout(pollCtx, consts.PollingInterval, consts.ResourceWaitTimeout, true, func(ctx context.Context) (bool, error) {
		svc, err := clientset.CoreV1().Services(namespace).Get(ctx, nfsServerPodName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
			return false, nil
		}
		clusterIP = svc.Spec.ClusterIP
		return true, nil
	})
	cancel()
	require.NoError(t, err, "timed out waiting for NFS service ClusterIP in namespace %s", namespace)
	helpers.Logf("NFS server ClusterIP in namespace %s: %s", namespace, clusterIP)

	// Create a local (no-provisioner) StorageClass scoped to this namespace.
	bindingMode := storagev1.VolumeBindingImmediate
	reclaimPolicy := corev1.PersistentVolumeReclaimDelete
	sc := &storagev1.StorageClass{
		TypeMeta:          metav1.TypeMeta{Kind: "StorageClass", APIVersion: "storage.k8s.io/v1"},
		ObjectMeta:        metav1.ObjectMeta{Name: scName},
		Provisioner:       "kubernetes.io/no-provisioner",
		VolumeBindingMode: &bindingMode,
		ReclaimPolicy:     &reclaimPolicy,
	}
	scYAML, err := yaml.Marshal(sc)
	require.NoError(t, err, "failed to marshal NFS StorageClass")
	defaultKubeOpts := k8s.NewKubectlOptions("", "", "")
	KubectlApplyFromString(ctx, t, defaultKubeOpts, string(scYAML))

	// createPV creates a static NFS PersistentVolume. path is relative to the NFSv4
	// pseudoroot (server exports /nfsshare with fsid=0, so server:/vmstorage/0 →
	// /nfsshare/vmstorage/0).
	createPV := func(pvName, nfsPath string, capacity resource.Quantity) {
		pv := &corev1.PersistentVolume{
			TypeMeta:   metav1.TypeMeta{Kind: "PersistentVolume", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: pvName},
			Spec: corev1.PersistentVolumeSpec{
				Capacity:                      corev1.ResourceList{corev1.ResourceStorage: capacity},
				AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
				StorageClassName:              scName,
				MountOptions:                  []string{"nfsvers=4"},
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: clusterIP,
						Path:   nfsPath,
					},
				},
			},
		}
		pvYAML, err := yaml.Marshal(pv)
		require.NoError(t, err, "failed to marshal NFS PersistentVolume %s", pvName)
		// Delete any stale PV — spec.persistentVolumeSource is immutable.
		k8s.RunKubectlContext(t, ctx, defaultKubeOpts, "delete", "pv", pvName,
			"--ignore-not-found=true", fmt.Sprintf("--timeout=%s", 60*time.Second))
		KubectlApplyFromString(ctx, t, defaultKubeOpts, string(pvYAML))
	}

	cacheQty := resource.MustParse(nfsCacheSize)

	for i := range nfsVMStorageReplicas {
		createPV(nfsVMStoragePVName(namespace, i), fmt.Sprintf("/vmstorage/%d", i), storageQty)
	}
	for i := range nfsVMSelectReplicas {
		createPV(nfsVMSelectPVName(namespace, i), fmt.Sprintf("/vmselect/%d", i), cacheQty)
	}

	return scName
}

// DeleteNFSResources removes the NFS StorageClass and PersistentVolumes created for
// a namespace. PVCs, the NFS pod, and the Service are cleaned up by namespace deletion.
func DeleteNFSResources(ctx context.Context, t terratesting.TestingT, namespace string) {
	defaultKubeOpts := k8s.NewKubectlOptions("", "", "")

	for i := range nfsVMStorageReplicas {
		pvName := nfsVMStoragePVName(namespace, i)
		helpers.Logf("Deleting NFS PersistentVolume %s", pvName)
		if err := k8s.RunKubectlContextE(t, ctx, defaultKubeOpts, "delete", "pv", pvName,
			"--ignore-not-found=true", fmt.Sprintf("--timeout=%s", 30*time.Second)); err != nil {
			helpers.Logf("WARNING: failed to delete NFS PersistentVolume %s: %v", pvName, err)
		}
	}
	for i := range nfsVMSelectReplicas {
		pvName := nfsVMSelectPVName(namespace, i)
		helpers.Logf("Deleting NFS PersistentVolume %s", pvName)
		if err := k8s.RunKubectlContextE(t, ctx, defaultKubeOpts, "delete", "pv", pvName,
			"--ignore-not-found=true", fmt.Sprintf("--timeout=%s", 30*time.Second)); err != nil {
			helpers.Logf("WARNING: failed to delete NFS PersistentVolume %s: %v", pvName, err)
		}
	}

	scName := nfsStorageClassName(namespace)
	helpers.Logf("Deleting NFS StorageClass %s", scName)
	if err := k8s.RunKubectlContextE(t, ctx, defaultKubeOpts, "delete", "storageclass", scName,
		"--ignore-not-found=true", fmt.Sprintf("--timeout=%s", 30*time.Second)); err != nil {
		helpers.Logf("WARNING: failed to delete NFS StorageClass %s: %v", scName, err)
	}
}

func boolPtr(b bool) *bool { return &b }
