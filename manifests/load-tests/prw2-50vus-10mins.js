import http from "k6/http";
import { check } from "k6";
import { randomIntBetween } from "https://jslib.k6.io/k6-utils/1.2.0/index.js";

export const options = {
  scenarios: {
    insert: {
      executor: "constant-vus",
      duration: "10m",
      vus: 50,
      exec: "insert",
    },
    read: {
      executor: "constant-vus",
      duration: "10m",
      vus: 50,
      exec: "read",
    },
  },
  insecureSkipTLSVerify: true,
};

const VMSELECT_URL = "http://vmselect-vmks.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range";
const VMINSERT_URL = "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/import/prometheus";
const VM_NAMESPACE = "monitoring";

// buildLine returns a Prometheus text exposition line for the given metric.
// Format: metric_name{label="value",...} numeric_value timestamp_ms
function buildLine(metricName, labels, value, timestampMs) {
  const labelStr = Object.entries(labels)
    .map(([k, v]) => `${k}="${v}"`)
    .join(",");
  return `${metricName}{${labelStr}} ${value} ${timestampMs}\n`;
}

function run_query(query) {
  const now = Date.now();
  const start = Math.floor((now - 10 * 60 * 1000) / 1000);
  const end = Math.floor(now / 1000);

  const res = http.post(VMSELECT_URL, { query: query, start: start, end: end, step: "15s" }, {});
  check(res, {
    "status is 200": (r) => r.status === 200,
  });
}

export function read() {
  const metricIdx = randomIntBetween(0, 9);
  run_query(`k6_metric_${metricIdx}{job="k6_load_test",namespace="${VM_NAMESPACE}"}`);
}

export function insert() {
  const metricIdx = randomIntBetween(0, 9);
  const line = buildLine(
    `k6_metric_${metricIdx}`,
    { instance: `vu-${__VU}`, job: "k6_load_test", namespace: VM_NAMESPACE },
    randomIntBetween(1, 10000),
    Date.now(),
  );

  const res = http.post(VMINSERT_URL, line, {
    headers: { "Content-Type": "text/plain" },
  });
  check(res, {
    "status is 204": (r) => r.status === 204,
  });
}
