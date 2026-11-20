// heavy-queries.js
//
// Steady PRW ingestion plus readers that only send expensive queries: wide
// name regexps, high-cardinality grouping and long-window rollups. The test
// caps vmselect concurrency, so readers must queue and get rejected while
// ingestion stays healthy.
import remote from 'k6/x/remotewrite';
import http from "k6/http";
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "5m";
const INSERT_RATE = Number(__ENV.K6_INSERT_RATE || 1000);
const READ_VUS = Number(__ENV.K6_READ_VUS || 30);
const BATCH_SIZE = Number(__ENV.K6_BATCH_SIZE || 100);
// Distinct label values per batch slot; bounds the series count the queries scan.
const SERIES_PER_METRIC = 5000;

export const options = {
  scenarios: {
    insert: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: INSERT_RATE,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "insert",
    },
    read: {
      executor: "constant-vus",
      duration: K6_DURATION,
      vus: READ_VUS,
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

const SEL = `job="k6_load_test",namespace="${VM_NAMESPACE}"`;
const HEAVY_QUERIES = [
  // Every series of every metric name.
  `sum(rate({__name__=~"k6_metric_.*",${SEL}}[5m]))`,
  // Output cardinality equals input cardinality.
  `count by(__name__, instance) ({__name__=~"k6_metric_.*",${SEL}})`,
  // Long rollup window over all raw samples.
  `quantile_over_time(0.99, {__name__=~"k6_metric_.*",${SEL}}[10m])`,
  // Sort the whole result set.
  `topk(10, max_over_time({__name__=~"k6_metric_.*",${SEL}}[10m]))`,
];

export function read() {
  const now = Math.floor(Date.now() / 1000);
  const query = HEAVY_QUERIES[Math.floor(Math.random() * HEAVY_QUERIES.length)];
  const res = http.post(
    VMSELECT_URL,
    { query: query, start: now - 30 * 60, end: now, step: "15s" },
    { responseType: "none", timeout: "60s" },
  );
  check(res, {
    "query status is 200": (r) => r.status === 200,
  });
}

export function insert() {
  const series = [];
  for (let i = 0; i < BATCH_SIZE; i++) {
    const idx = (__ITER * BATCH_SIZE + i) % SERIES_PER_METRIC;
    series.push(
      remote.Timeseries(
        {
          __name__: `k6_metric_${i % 10}`,
          instance: `inst_${idx}`,
          job: "k6_load_test",
          namespace: VM_NAMESPACE,
        },
        [remote.Sample(Math.random() * 1000, Date.now())],
      ),
    );
  }
  const res = client.store(series);
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
