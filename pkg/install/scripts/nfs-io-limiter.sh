set -eux

cgroup=$(awk -F: '$1 == "0" {print $3}' /proc/1/cgroup)
test -n "$cgroup"
cgroup_dir="/sys/fs/cgroup$cgroup"
test -f "$cgroup_dir/io.max"
echo "cgroup_dir=$cgroup_dir"

# Ground truth: the device actually backing the shared NFS export volume, read
# from this container's own mountinfo (it mounts the same emptyDir as the NFS
# server). Used below only to flag a mismatch — if the device picked from
# io.stat differs, the limit is being applied to the wrong disk.
export_dev=$(awk -v p="$NFS_EXPORT_PATH" '$5 == p {print $3; exit}' /proc/self/mountinfo)
echo "export_dev(mountinfo for $NFS_EXPORT_PATH)=${export_dev:-unknown}"

# NFS kernel-server I/O may not be charged to pod cgroup on GKE. Fall back to
# host root io.stat, where backing device is visible.
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

echo "picked device=$device via $source"
if test -n "$export_dev" && test "$device" != "$export_dev"; then
  echo "WARNING: picked device ($device, via $source) != export volume device ($export_dev) -- limit may be applied to the wrong disk"
fi

echo "$device rbps=$NFS_IO_BYTES_PER_SECOND wbps=$NFS_IO_BYTES_PER_SECOND riops=$NFS_IOPS wiops=$NFS_IOPS" > "$cgroup_dir/io.max"
cat "$cgroup_dir/io.max"

# Periodic snapshots so `kubectl logs` shows the device pick is still correct
# and whether observed throughput stays within the configured cap for the life
# of the test.
while :; do
  sleep 30
  echo "--- $(date -u +%FT%TZ) ---"
  echo "cgroup_dir=$cgroup_dir picked device=$device via $source export_dev=${export_dev:-unknown}"
  if test -n "$export_dev" && test "$device" != "$export_dev"; then
    echo "WARNING: picked device ($device, via $source) != export volume device ($export_dev) -- limit may be applied to the wrong disk"
  fi
  echo "io.max: $(cat "$cgroup_dir/io.max")"
  echo "io.stat[$device]: $(awk -v d="$device" '$1 == d' "$cgroup_dir/io.stat")"
done
