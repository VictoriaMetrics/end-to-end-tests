import http from "k6/http";
import { check } from "k6";
import { randomIntBetween } from "https://jslib.k6.io/k6-utils/1.2.0/index.js";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";

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

// VMINSERT_URL must point to the global write VMAuth ingress created by VMDistributed.
// It accepts /api/v1/import/.+ and /insert/.+ paths and fans out writes to all availability zones.
const VMINSERT_URL = __ENV.VMINSERT_URL;
// VMSELECT_URL must point to the global read VMAuth ingress created by VMDistributed.
// It accepts /select/.+ paths and load-balances reads across all availability zones.
const VMSELECT_URL = __ENV.VMSELECT_URL;
const VM_NAMESPACE = __ENV.VM_NAMESPACE || "monitoring";

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
  const res = http.post(VMINSERT_URL, line, { headers: { "Content-Type": "text/plain" } });
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
