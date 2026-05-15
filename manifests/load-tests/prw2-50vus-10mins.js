import http from "k6/http";
import { check } from "k6";
import { randomIntBetween } from "https://jslib.k6.io/k6-utils/1.2.0/index.js";
import { RemoteWrite } from "k6/experimental/prometheus";

const K6_DURATION = __ENV.K6_DURATION || "10m";

export const options = {
  scenarios: {
    insert: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 150,
      timeUnit: "1s",
      preAllocatedVUs: 50,
      maxVUs: 500,
      exec: "insert",
    },
    read: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 40,
      timeUnit: "1s",
      preAllocatedVUs: 50,
      maxVUs: 500,
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

const rwClient = new RemoteWrite({
  url: VMINSERT_URL,
});

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

  try {
    rwClient.store([
      {
        name: `k6_metric_${metricIdx}`,
        labels: { instance: `vu-${__VU}`, job: "k6_load_test", namespace: VM_NAMESPACE },
        value: randomIntBetween(1, 10000),
        timestamp: Date.now(),
      },
    ]);
  } catch (e) {
    console.error(e);
  }
}
