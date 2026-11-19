// vmagent-queue-overflow.js
//
// Steady remote-write stream into VMAgent. One of its remote-write URLs is
// unreachable, so pending data piles up in that URL's persistent queue until
// -remoteWrite.maxDiskUsagePerURL is hit and VMAgent starts dropping blocks.
//
// The series set is fixed (no churn for the healthy VMCluster); random values
// and long random label values keep blocks poorly compressible so the queue
// fills within the scenario.
import remote from 'k6/x/remotewrite';
import { check } from "k6";
import { sha512 } from "k6/crypto";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";
const INSERT_RATE = Number(__ENV.K6_INSERT_RATE || 1000);
const BATCH_SIZE = Number(__ENV.K6_BATCH_SIZE || 100);
const SERIES_POOL = 20000;

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
  },
  insecureSkipTLSVerify: true,
};

const VMINSERT_URL =
  __ENV.VMINSERT_URL ||
  "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/write";
const VM_NAMESPACE = __ENV.VM_NAMESPACE || "monitoring";

const client = new remote.Client({ url: VMINSERT_URL });

// Labels are built per row instead of a precomputed pool: init code runs in
// every VU, and a 20k-series pool per VU OOMKilled the 512Mi k6 runners.
// pad is 128 random-looking hex chars (sha512 of the index), identical in
// every VU and k6 pod, so all pods share one series set. Its size bloats
// every row in the (snappy-compressed) queue so 512MiB fills in minutes.
function seriesLabels(idx) {
  return {
    __name__: `k6_queue_metric_${idx % 10}`,
    series: String(idx),
    pad: sha512(String(idx), "hex"),
    job: "k6_load_test",
    namespace: VM_NAMESPACE,
  };
}

export function insert() {
  const series = [];
  for (let i = 0; i < BATCH_SIZE; i++) {
    const labels = seriesLabels(Math.floor(Math.random() * SERIES_POOL));
    series.push(remote.Timeseries(labels, [remote.Sample(Math.random() * 1e6, Date.now())]));
  }
  const res = client.store(series);
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export default function () {}
