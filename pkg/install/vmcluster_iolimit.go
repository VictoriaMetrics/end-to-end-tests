package install

import (
	_ "embed"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

const (
	// vmstorageIOBytesPerSecond/vmstorageIOPS/vmselectIOBytesPerSecond/vmselectIOPS
	// are tight enough relative to baseline's normal throughput that the cap
	// visibly bottlenecks the scenario. Measured on a live run: vmstorage
	// disk writes peaked ~0.85MB/s / ~5.4 IOPS, vmselect ~1.5MB/s / ~6.2 IOPS
	// -- both 5MiB/s+200IOPS and 50MiB/s+2000IOPS sat above actual usage and
	// never throttled anything. Caps below are set under those observed
	// peaks so io.max actually queues writes.
	vmstorageIOBytesPerSecond = 300 * 1024 // 300 KiB/s
	vmstorageIOPS             = 5
	vmselectIOBytesPerSecond  = 500 * 1024 // 500 KiB/s
	vmselectIOPS              = 5

	// Operator's implicit EmptyDir volume names for the default (unset
	// storage) data/cache dirs, confirmed live on operator v0.74.1 via
	// `kubectl get pod -o jsonpath='{.spec.volumes[*].name}'`. Not part of
	// the public API/contract -- may change on operator upgrade. These sit
	// on dedicated node-local disks, separate from the node's root disk, so
	// device detection must mount the real volume, not a throwaway emptyDir.
	vmstorageDataVolumeName = "vmstorage-db"
	vmstorageDataMountPath  = "/vm-data"
	vmselectCacheVolumeName = "vmselect-cachedir"
	vmselectCacheMountPath  = "/select-cache"
)

//go:embed scripts/pod-io-limiter.sh
var podIOLimiterScript string

// ioLimiterSidecar builds a privileged sidecar container that caps the pod's
// aggregate disk I/O via a cgroup v2 io.max written on the pod-level slice
// (rolling up the sidecar's own leaf cgroup and the main component
// container's leaf cgroup). It mounts the component's real data/cache volume
// (already defined in the pod spec by the operator) read-only, purely to
// resolve the backing block device via mountinfo.
func ioLimiterSidecar(bytesPerSecond, iops int, dataVolumeName, dataMountPath string) corev1.Container {
	return corev1.Container{
		Name:    "io-limiter",
		Image:   "alpine:3.22",
		Command: []string{"sh", "-c", podIOLimiterScript},
		Env: []corev1.EnvVar{
			{Name: "POD_UID", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"},
			}},
			{Name: "TARGET_MOUNT_PATH", Value: dataMountPath},
			{Name: "IO_BYTES_PER_SECOND", Value: fmt.Sprint(bytesPerSecond)},
			{Name: "IO_IOPS", Value: fmt.Sprint(iops)},
		},
		SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)},
		VolumeMounts: []corev1.VolumeMount{
			{Name: dataVolumeName, MountPath: dataMountPath, ReadOnly: true},
			{Name: "cgroup", MountPath: "/sys/fs/cgroup"},
		},
	}
}

func ioLimiterVolumes() []corev1.Volume {
	return []corev1.Volume{
		{Name: "cgroup", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys/fs/cgroup"}}},
	}
}

// VMClusterIOLimitPatches returns JSON patches that inject a cgroup-based disk
// I/O rate limiter sidecar into vmstorage and vmselect pods.
func VMClusterIOLimitPatches() ([]jsonpatch.Patch, error) {
	vmstorageSidecar := ioLimiterSidecar(vmstorageIOBytesPerSecond, vmstorageIOPS, vmstorageDataVolumeName, vmstorageDataMountPath)
	vmselectSidecar := ioLimiterSidecar(vmselectIOBytesPerSecond, vmselectIOPS, vmselectCacheVolumeName, vmselectCacheMountPath)

	patch, err := CreateJsonPatch([]PatchOp{
		{Op: "add", Path: "/spec/vmstorage/containers", Value: []corev1.Container{vmstorageSidecar}},
		{Op: "add", Path: "/spec/vmstorage/volumes", Value: ioLimiterVolumes()},
		{Op: "add", Path: "/spec/vmselect/containers", Value: []corev1.Container{vmselectSidecar}},
		{Op: "add", Path: "/spec/vmselect/volumes", Value: ioLimiterVolumes()},
	})
	if err != nil {
		return nil, err
	}
	return []jsonpatch.Patch{patch}, nil
}
