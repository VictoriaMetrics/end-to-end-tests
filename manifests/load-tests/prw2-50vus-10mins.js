import remote from 'k6/x/remotewrite';
import faker from 'k6/x/faker';
import http from "k6/http";
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";
const INSERT_RATE = Number(__ENV.K6_INSERT_RATE || 5000);
const READ_RATE = Number(__ENV.K6_READ_RATE || 1400);
const BATCH_SIZE = Number(__ENV.K6_BATCH_SIZE || 1);

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
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: READ_RATE,
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
  const metricIdx = BATCH_SIZE > 1 ? Math.floor(Math.random() * 10) : faker.numbers.intRange(0, 9);
  run_query(
    `sum by(first_name, last_name) (rate(k6_metric_${metricIdx}{job="k6_load_test",namespace="${VM_NAMESPACE}"}[5m]))`,
  );
}

export function insert() {
  const series = [];
  const seriesPrefix = `${__VU}_${__ITER}`;
  for (let i = 0; i < BATCH_SIZE; i++) {
    const metricIdx = BATCH_SIZE > 1 ? (i + __ITER) % 10 : faker.numbers.intRange(0, 9);
    series.push(
      remote.Timeseries(
        {
          __name__: `k6_metric_${metricIdx}`,
          first_name: BATCH_SIZE > 1 ? `${seriesPrefix}_${i}` : faker.person.firstName(),
          last_name: BATCH_SIZE > 1 ? "load" : faker.person.lastName(),
          job: "k6_load_test",
          namespace: VM_NAMESPACE,
        },
        [remote.Sample(BATCH_SIZE > 1 ? (i % 10000) + 1 : faker.numbers.intRange(1, 10000), Date.now())],
      ),
    );
  }
  const res = client.store(series);
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
