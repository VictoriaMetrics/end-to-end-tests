import remote from 'k6/x/remotewrite';
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";

export const options = {
  scenarios: {
    write: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 1500,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "write",
    },
  },
  insecureSkipTLSVerify: true,
};

const VMINSERT_URL =
  __ENV.VMINSERT_URL ||
  "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/write";
const VM_NAMESPACE = __ENV.VM_NAMESPACE || "monitoring";

const client = new remote.Client({ url: VMINSERT_URL });

// 10 metric names (series_id 0-149 → metric names 0-9 via /15)
// series_id maps directly to VU number for per-instance cardinality
const compiled = remote.precompileLabelTemplates({
  __name__: 'k6_metric_${series_id/15}',
  instance: 'vu-${series_id}',
  job: 'k6_load_test',
  namespace: VM_NAMESPACE,
});

export function write() {
  const seriesId = __VU % 150;
  const res = client.storeFromPrecompiledTemplates(
    1, 10000, Date.now(),
    seriesId, seriesId + 1,
    compiled,
  );
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
