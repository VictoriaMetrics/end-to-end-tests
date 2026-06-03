import remote from 'k6/x/remotewrite';
import faker from 'k6/x/faker';
import http from "k6/http";
import { check } from "k6";

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
  const metricIdx = faker.numbers.intRange(0, 9);
  run_query(`k6_metric_${metricIdx}{job="k6_load_test",namespace="${VM_NAMESPACE}"}`);
}

export function insert() {
  const metricIdx = faker.numbers.intRange(0, 9);
  const res = client.store([
    remote.Timeseries(
      {
        __name__: `k6_metric_${metricIdx}`,
        first_name: faker.person.firstName(),
        last_name: faker.person.lastName(),
        job: "k6_load_test",
        namespace: VM_NAMESPACE,
      },
      [remote.Sample(faker.numbers.intRange(1, 10000), Date.now())],
    ),
  ]);
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
