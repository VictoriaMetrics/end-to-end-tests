set -eux

cgroup=$(awk -F: '$1 == "0" {print $3}' /proc/1/cgroup)
test -n "$cgroup"
cgroup_dir="/sys/fs/cgroup$cgroup"
test -f "$cgroup_dir/io.max"

# NFS kernel-server I/O may not be charged to pod cgroup on GKE. Fall back to
# host root io.stat, where backing device is visible.
while :; do
  device=$(awk '$1 ~ /^[0-9]+:[0-9]+$/ && ($2 ~ /rbytes=[1-9]/ || $3 ~ /wbytes=[1-9]/) {print $1; exit}' "$cgroup_dir/io.stat")

  if test -z "$device"; then
    device=$(awk '$1 ~ /^[0-9]+:[0-9]+$/ && ($2 ~ /rbytes=[1-9]/ || $3 ~ /wbytes=[1-9]/) {print $1; exit}' /sys/fs/cgroup/io.stat)
  fi

  test -n "$device" && break
  sleep 1
done

echo "$device rbps=$NFS_IO_BYTES_PER_SECOND wbps=$NFS_IO_BYTES_PER_SECOND riops=$NFS_IOPS wiops=$NFS_IOPS" > "$cgroup_dir/io.max"
cat "$cgroup_dir/io.max"
sleep infinity
