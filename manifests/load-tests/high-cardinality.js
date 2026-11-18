// high-cardinality.js
//
// Every sample belongs to a series never seen before (unique `id` label per
// VU, iteration and batch slot), so vmstorage registers a new series for each
// row: worst-case churn and index growth.
import remote from 'k6/x/remotewrite';
import { check } from "k6";
import exec from "k6/execution";

const K6_DURATION = __ENV.SCENARIO_DURATION || "5m";
const INSERT_RATE = Number(__ENV.K6_INSERT_RATE || 200);
const BATCH_SIZE = Number(__ENV.K6_BATCH_SIZE || 50);

export const options = {
  scenarios: {
    insert: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: INSERT_RATE,
      timeUnit: "1s",
      preAllocatedVUs: 50,
      maxVUs: 100,
      exec: "insert",
    },
  },
  insecureSkipTLSVerify: true,
};

const VMINSERT_URL =
  __ENV.VMINSERT_URL ||
  "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/write";
const VM_NAMESPACE = __ENV.VM_NAMESPACE || "monitoring";

const client = new remote.Client({ url: VMINSERT_URL });

export function insert() {
  const series = [];
  // Unique across parallel k6 pods: idInTest is global, __VU is per pod.
  const prefix = `${exec.vu.idInTest}_${exec.vu.iterationInInstance}`;
  for (let i = 0; i < BATCH_SIZE; i++) {
    series.push(
      remote.Timeseries(
        {
          __name__: `k6_churn_metric_${i % 10}`,
          id: `${prefix}_${i}`,
          job: "k6_load_test",
          namespace: VM_NAMESPACE,
        },
        [remote.Sample(i, Date.now())],
      ),
    );
  }
  const res = client.store(series);
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
