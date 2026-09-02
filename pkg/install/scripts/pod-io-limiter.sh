set -eux

# Resolve this pod's cgroup slice via its UID (Downward API): the VMCluster CRD
# exposes no ShareProcessNamespace/hostPID field, so unlike the NFS sidecar we
# cannot read /proc/1/cgroup of the main container. Kubernetes embeds the pod
# UID (dashes -> underscores) in the per-pod slice directory name, so we can
# find it directly under /sys/fs/cgroup instead.
pod_uid_us=$(echo "$POD_UID" | tr '-' '_')

pod_slice=""
for _ in $(seq 1 30); do
  pod_slice=$(find /sys/fs/cgroup -maxdepth 4 -type d -name "*pod${pod_uid_us}*" 2>/dev/null | head -1)
  test -n "$pod_slice" && break
  sleep 1
done
test -n "$pod_slice"
test -f "$pod_slice/io.max"
echo "pod_slice=$pod_slice"

# Resolve the whole-disk device backing our own scratch emptyDir. All emptyDir
# volumes on a node share the same backing device (the node's ephemeral-storage
# disk), so this sidecar-owned scratch volume resolves to the same device as
# the component's real data/cache dir without needing to know the operator's
# internal volume name for it. cgroup v2 io.max only accepts whole-disk devices
# with their own request queue — partitions share their parent disk's queue and
# error with "No such device" — so walk up to the parent if needed.
target_dev=$(awk -v p="$TARGET_MOUNT_PATH" '$5 == p {print $3; exit}' /proc/self/mountinfo)
test -n "$target_dev"
echo "target_dev(mountinfo for $TARGET_MOUNT_PATH)=$target_dev"

dev_path=$(readlink -f "/sys/dev/block/$target_dev")
if test -f "$dev_path/partition"; then
  device=$(cat "$(dirname "$dev_path")/dev")
  echo "target_dev=$target_dev is a partition, whole disk=$device"
else
  device="$target_dev"
fi

# Writing io.max on this pod-level (non-leaf) cgroup is valid: cgroup v2's "no
# internal process constraint" only restricts which cgroups may hold live
# tasks, not which may carry resource-control files. It caps the aggregate of
# every container in the pod (the component's own leaf .scope + this
# sidecar's), which is correct here since — unlike NFS's kernel nfsd threads —
# the component's read/write syscalls are issued by its own container process
# and attributed to its own leaf cgroup by the kernel, rolling up normally
# into this parent.
apply_limit() {
  echo "$device rbps=$IO_BYTES_PER_SECOND wbps=$IO_BYTES_PER_SECOND riops=$IO_IOPS wiops=$IO_IOPS" > "$pod_slice/io.max"
}

apply_limit
echo "applied io.max on $pod_slice for device=$device: $(cat "$pod_slice/io.max")"

# Periodic re-apply + snapshot so `kubectl logs` shows the limit stayed in
# effect and how observed throughput compares to the configured cap.
while :; do
  sleep 30
  echo "--- $(date -u +%FT%TZ) ---"
  apply_limit
  echo "$pod_slice/io.max: $(cat "$pod_slice/io.max")"
  echo "io.stat[$device]: $(awk -v d="$device" '$1 == d' "$pod_slice/io.stat")"
done
