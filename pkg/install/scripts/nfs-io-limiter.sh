set -eux

cgroup=$(awk -F: '$1 == "0" {print $3}' /proc/1/cgroup)
test -n "$cgroup"
cgroup_dir="/sys/fs/cgroup$cgroup"
test -f "$cgroup_dir/io.max"
echo "cgroup_dir=$cgroup_dir"

# Ground truth: the device actually backing the shared NFS export volume, read
# from this container's own mountinfo (it mounts the same emptyDir as the NFS
# server). This is often a partition (e.g. 8:1), but cgroup v2 io.max only
# accepts whole-disk devices with their own request queue — partitions share
# their parent disk's queue and error with "No such device". Resolve up to the
# whole-disk parent via sysfs before using it.
export_dev=$(awk -v p="$NFS_EXPORT_PATH" '$5 == p {print $3; exit}' /proc/self/mountinfo)
echo "export_dev(mountinfo for $NFS_EXPORT_PATH)=${export_dev:-unknown}"

whole_disk_dev=""
if test -n "$export_dev"; then
  dev_path=$(readlink -f "/sys/dev/block/$export_dev" || true)
  if test -n "$dev_path" && test -f "$dev_path/partition"; then
    whole_disk_dev=$(cat "$(dirname "$dev_path")/dev")
    echo "export_dev=$export_dev is a partition, whole disk=$whole_disk_dev"
  else
    whole_disk_dev="$export_dev"
  fi
fi

if test -n "$whole_disk_dev"; then
  device="$whole_disk_dev"
  source="export volume mountinfo (whole disk)"
else
  # Fallback only: mountinfo lookup failed, guess via io.stat activity. NFS
  # kernel-server I/O may not be charged to the pod cgroup on GKE, so fall
  # back further to host root io.stat, where backing device is visible.
  echo "WARNING: could not resolve export device from mountinfo, falling back to io.stat activity scan"
  while :; do
    echo "--- container io.stat ---"
    cat "$cgroup_dir/io.stat" || true
    device=$(awk '$1 ~ /^[0-9]+:[0-9]+$/ && ($2 ~ /rbytes=[1-9]/ || $3 ~ /wbytes=[1-9]/) {print $1; exit}' "$cgroup_dir/io.stat")
    source="container cgroup"

    if test -z "$device"; then
      echo "--- host root io.stat ---"
      cat /sys/fs/cgroup/io.stat || true
      device=$(awk '$1 ~ /^[0-9]+:[0-9]+$/ && ($2 ~ /rbytes=[1-9]/ || $3 ~ /wbytes=[1-9]/) {print $1; exit}' /sys/fs/cgroup/io.stat)
      source="host root fallback"
    fi

    test -n "$device" && break
    echo "no active device found yet, retrying..."
    sleep 1
  done
fi

echo "picked device=$device via $source"

# cgroup v2's root cgroup has no io.max of its own (a cgroup can't limit itself,
# only its descendants), and kernel nfsd worker threads are kthreads that are
# never migrated into the nfs-server container's cgroup — so a limit written
# only to $cgroup_dir/io.max never throttles nfsd's actual block I/O. The
# closest available approximation to a true node-wide cap is applying the same
# limit to every cgroup on the node that exposes io.max: together they cover
# every process (and, transitively, nfsd's writeback), throttling the shared
# physical device rather than one pod's traffic.
apply_node_wide_limit() {
  find /sys/fs/cgroup -mindepth 1 -maxdepth 8 -name io.max 2>/dev/null | while read -r f; do
    echo "$device rbps=$NFS_IO_BYTES_PER_SECOND wbps=$NFS_IO_BYTES_PER_SECOND riops=$NFS_IOPS wiops=$NFS_IOPS" > "$f" 2>/dev/null || true
  done
}

apply_node_wide_limit
echo "applied node-wide io.max for device=$device:"
find /sys/fs/cgroup -mindepth 1 -maxdepth 8 -name io.max 2>/dev/null | while read -r f; do
  echo "$f: $(cat "$f")"
done

# Periodic re-apply + snapshot so `kubectl logs` shows the device pick is still
# correct, new per-pod cgroups (created after this pass) also get capped, and
# whether observed throughput stays within the configured cap for the test.
while :; do
  sleep 30
  echo "--- $(date -u +%FT%TZ) ---"
  echo "picked device=$device via $source export_dev=${export_dev:-unknown} whole_disk_dev=${whole_disk_dev:-unknown}"
  if test -n "$whole_disk_dev" && test "$device" != "$whole_disk_dev"; then
    echo "WARNING: picked device ($device, via $source) != resolved whole disk device ($whole_disk_dev) -- limit may be applied to the wrong disk"
  fi
  apply_node_wide_limit
  find /sys/fs/cgroup -mindepth 1 -maxdepth 8 -name io.max 2>/dev/null | while read -r f; do
    echo "$f: $(cat "$f")"
  done
  echo "io.stat[$device] (host root): $(awk -v d="$device" '$1 == d' /sys/fs/cgroup/io.stat)"
done
