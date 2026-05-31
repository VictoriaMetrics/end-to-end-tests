import http from "k6/http";
import { check } from "k6";
import { randomIntBetween } from "https://jslib.k6.io/k6-utils/1.2.0/index.js";

export const options = {
  scenarios: {
    insert: {
      executor: "ramping-arrival-rate",
      startRate: 0,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "insert",
      stages: [
        { duration: "7m", target: 50000 },
        { duration: "3m", target: 0 },
      ],
    },
    read: {
      executor: "ramping-arrival-rate",
      startRate: 0,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "read",
      stages: [
        { duration: "7m", target: 14000 },
        { duration: "3m", target: 0 },
      ],
    },
  },
  insecureSkipTLSVerify: true,
};

const VMSELECT_URL =
  __ENV.VMSELECT_URL ||
  "http://vmselect-vmks.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range";
const VMINSERT_URL =
  __ENV.VMINSERT_URL ||
  "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/import/prometheus";
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
  const metricIdx = randomIntBetween(0, 9);
  run_query(`k6_metric_${metricIdx}{job="k6_load_test",namespace="${VM_NAMESPACE}"}`);
}

export function insert() {
  const metricIdx = randomIntBetween(0, 9);
  const seriesIdx = randomIntBetween(0, 9999);
  const minuteBucket = Math.floor(Date.now() / 60000);
  const line = buildLine(
    `k6_metric_${metricIdx}`,
    { series: `s-${minuteBucket}-${seriesIdx}`, job: "k6_load_test", namespace: VM_NAMESPACE },
    randomIntBetween(1, 10000),
    Date.now(),
  );
  const res = http.post(VMINSERT_URL, line, {
    headers: { "Content-Type": "text/plain" },
    responseType: "none",
  });
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
