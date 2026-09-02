set -eux

cgroup=$(awk -F: '$1 == "0" {print $3}' /proc/1/cgroup)
test -n "$cgroup"
cgroup_dir="/sys/fs/cgroup$cgroup"
test -f "$cgroup_dir/io.max"

# Device may be absent until NFS clients issue first I/O during pod startup.
while :; do
  device=$(awk '$1 ~ /^[0-9]+:[0-9]+$/ && ($2 ~ /rbytes=[1-9]/ || $3 ~ /wbytes=[1-9]/) {print $1; exit}' "$cgroup_dir/io.stat")
  test -n "$device" && break
  sleep 1
done

echo "$device rbps=$NFS_IO_BYTES_PER_SECOND wbps=$NFS_IO_BYTES_PER_SECOND riops=$NFS_IOPS wiops=$NFS_IOPS" > "$cgroup_dir/io.max"
cat "$cgroup_dir/io.max"
sleep infinity
