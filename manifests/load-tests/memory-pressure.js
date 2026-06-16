// memory-pressure.js
//
// Three-phase load test designed to reproduce and validate vminsert memory
// behaviour under increasing concurrency and batch sizes (issue #542).
//
// Phase 1 – Warm-up  (0–3 min):
//   25 concurrent VUs, ~9 k rows/request, low series churn.
//
// Phase 2 – Migration load (3–9 min):
//   80 concurrent VUs, ~11 k rows/request, moderate churn.
//
// Phase 3 – Burst / catch-up (9–13 min):
//   115 concurrent VUs, ~15 k rows/request, high new-series spike.
//
// A lightweight read workload runs throughout all phases to exercise
// vmselect alongside vminsert pressure.
//
// Key env vars (all optional):
//   VMINSERT_URL   – remote-write endpoint (default: cluster-local vminsert).
//   VMSELECT_URL   – query endpoint        (default: cluster-local vmselect).
//   VM_NAMESPACE   – label injected into every series.

import remote from 'k6/x/remotewrite';
import http from 'k6/http';
import { check } from 'k6';

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const VMINSERT_URL =
  __ENV.VMINSERT_URL ||
  'http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/write';
const VMSELECT_URL =
  __ENV.VMSELECT_URL ||
  'http://vmselect-vmks.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range';
const VM_NAMESPACE = __ENV.VM_NAMESPACE || 'monitoring';

// Rows (timeseries) sent per remote-write request in each phase.
const ROWS_WARMUP     = 9000;
const ROWS_MIGRATION  = 11000;
const ROWS_BURST      = 15000;

// Number of distinct metric name buckets (low churn baseline).
const METRIC_BUCKETS  = 100;
// Number of distinct host labels (low churn baseline).
const HOST_BUCKETS    = 50;

// ---------------------------------------------------------------------------
// k6 scenario definitions
// ---------------------------------------------------------------------------

export const options = {
  scenarios: {
    // Phase 1: warm-up
    warmup_insert: {
      executor: 'constant-vus',
      vus: 25,
      duration: '3m',
      startTime: '0s',
      exec: 'insert_warmup',
    },
    // Phase 2: migration load
    migration_insert: {
      executor: 'constant-vus',
      vus: 80,
      duration: '6m',
      startTime: '3m',
      exec: 'insert_migration',
    },
    // Phase 3: burst / catch-up with high new-series spike
    burst_insert: {
      executor: 'constant-vus',
      vus: 115,
      duration: '4m',
      startTime: '9m',
      exec: 'insert_burst',
    },
    // Continuous read workload throughout all phases.
    read: {
      executor: 'constant-arrival-rate',
      rate: 800,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 80,
      duration: '13m',
      startTime: '0s',
      exec: 'read',
    },
  },
  insecureSkipTLSVerify: true,
};

// ---------------------------------------------------------------------------
// Remote-write client (shared across all insert functions)
// ---------------------------------------------------------------------------

const client = new remote.Client({ url: VMINSERT_URL });

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildBatch constructs an array of `size` remote.Timeseries objects.
//
// When highChurn is false the series set is bounded: metric names cycle
// through METRIC_BUCKETS, host labels through HOST_BUCKETS.  This produces
// a stable, low-churn workload.
//
// When highChurn is true every timeseries gets a unique churn_id derived
// from __VU and __ITER, forcing vminsert to allocate new-series metadata on
// every request – reproducing the new-series spike in the burst phase.
function buildBatch(size, highChurn) {
  const batch = [];
  const ts = Date.now();
  const vuStr  = String(__VU);
  const iterStr = String(__ITER);

  for (let i = 0; i < size; i++) {
    const labels = {
      __name__:  `k6_mem_${i % METRIC_BUCKETS}`,
      job:       'k6_mem_test',
      namespace: VM_NAMESPACE,
      host:      `host-${i % HOST_BUCKETS}`,
      region:    `region-${i % 5}`,
    };
    if (highChurn) {
      // Unique per VU × iteration × row index → forces a new timeseries each time.
      labels.churn_vu   = vuStr;
      labels.churn_iter = iterStr;
      labels.churn_idx  = String(i);
    }
    batch.push(remote.Timeseries(labels, [remote.Sample(i + 1, ts)]));
  }
  return batch;
}

function sendBatch(batch) {
  const res = client.store(batch);
  check(res, {
    'insert status is 2xx': (r) => r.status >= 200 && r.status < 300,
  });
}

// ---------------------------------------------------------------------------
// Insert executors (one per phase)
// ---------------------------------------------------------------------------

// Phase 1: low churn, 9 k rows/request.
export function insert_warmup() {
  sendBatch(buildBatch(ROWS_WARMUP, false));
}

// Phase 2: moderate churn (every other VU generates new series).
export function insert_migration() {
  const highChurn = __VU % 2 === 0;
  sendBatch(buildBatch(ROWS_MIGRATION, highChurn));
}

// Phase 3: maximum churn – every row is a new series.
export function insert_burst() {
  sendBatch(buildBatch(ROWS_BURST, true));
}

// ---------------------------------------------------------------------------
// Read workload (lightweight range queries)
// ---------------------------------------------------------------------------

export function read() {
  const now   = Date.now();
  const start = Math.floor((now - 10 * 60 * 1000) / 1000);
  const end   = Math.floor(now / 1000);
  const metricIdx = __VU % METRIC_BUCKETS;

  const res = http.post(
    VMSELECT_URL,
    {
      query: `sum by(host) (rate(k6_mem_${metricIdx}{job="k6_mem_test",namespace="${VM_NAMESPACE}"}[5m]))`,
      start: start,
      end:   end,
      step:  '15s',
    },
    { responseType: 'none' },
  );
  check(res, {
    'query status is 200': (r) => r.status === 200,
  });
}

export default function () {}
