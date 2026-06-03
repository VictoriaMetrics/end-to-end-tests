import remote from 'k6/x/remotewrite';
import faker from 'k6/x/faker';
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

export function write() {
  const metricIdx = __VU % 10;
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
