import remote from 'k6/x/remotewrite';
import http from "k6/http";
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";

export const options = {
  scenarios: {
    insert: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 5000,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "insert",
    },
    read: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 1400,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "read",
    },
  },
  insecureSkipTLSVerify: true,
};

const VMSELECT_URL =
  __ENV.VMSELECT_URL ||
  "http://vmselect-vmks.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range";
const VMINSERT_URL =
  __ENV.VMINSERT_URL ||
  "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/write";
const VM_NAMESPACE = __ENV.VM_NAMESPACE || "monitoring";

const client = new remote.Client({ url: VMINSERT_URL });

// 10 metric names (series_id 0-99999 → metric names 0-9)
// 100000 unique series total
const compiled = remote.precompileLabelTemplates({
  __name__: 'k6_metric_${series_id/10000}',
  series_id: '${series_id}',
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
  run_query(
    `sum by(series_id) (rate(k6_metric_${metricIdx}{job="k6_load_test",namespace="${VM_NAMESPACE}"}[5m]))`,
  );
}

export function insert() {
  const seriesId = Math.floor(Math.random() * 100000);
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
