import remote from 'k6/x/remotewrite';
import http from "k6/http";
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";

export const options = {
  scenarios: {
    insert: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 1500,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "insert",
    },
    read: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 400,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "read",
    },
  },
  insecureSkipTLSVerify: true,
};

// VMINSERT_URL must point to the global write VMAuth ingress created by VMDistributed.
// VMAuth routes /insert/.+ to vmagent which fans out writes to all availability zones.
const VMINSERT_URL = __ENV.VMINSERT_URL;
// VMSELECT_URL must point to the global read VMAuth ingress created by VMDistributed.
// It accepts /select/.+ paths and load-balances reads across all availability zones.
const VMSELECT_URL = __ENV.VMSELECT_URL;
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

function run_query(query) {
  const now = Date.now();
  const start = Math.floor((now - 10 * 60 * 1000) / 1000);
  const end = Math.floor(now / 1000);

  const res = http.post(
    VMSELECT_URL,
    { query: query, start: start, end: end, step: "15s" },
    { responseType: "none" },
  );
  check(res, {
    "query status is 200": (r) => r.status === 200,
  });
}

export function read() {
  const metricIdx = Math.floor(Math.random() * 10);
  run_query(`k6_metric_${metricIdx}{job="k6_load_test",namespace="${VM_NAMESPACE}"}`);
}

export function insert() {
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
