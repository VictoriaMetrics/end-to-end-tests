// vmagent-slow-clients.js
//
// Reproduces the scenario where slow or idle clients hold VMAgent insert
// slots, causing normal remote-write traffic to queue or time out.
//
// VMAgent is configured with a low -maxConcurrentInserts cap (set via
// extraArgs in the test entry) so slot exhaustion is observable at moderate
// concurrency.
//
// Timeline:
//   0–2m   Baseline: only normal clients. Establishes healthy p95 / error-rate.
//   2–12m  Pressure: slot-occupier VUs join. Each sends a large batch
//          continuously, holding insert slots for the full processing RTT.
//          Normal clients should observe latency spikes and/or 429s.
//   12–15m Recovery: slot-occupier VUs stop. Normal clients recover.
//
// Key env vars (all optional):
//   VMINSERT_URL        – remote-write endpoint (routed through VMAgent).
//   VMSELECT_URL        – query endpoint.
//   VM_NAMESPACE        – label injected into every series.
//   SLOW_CLIENT_ROWS    – rows per slot-occupier request (default 8000).

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

// Large batch size — each slot-occupier request forces VMAgent to parse and
// buffer significantly more data, extending per-request processing time and
// keeping the insert-slot semaphore held for longer.
const SLOW_CLIENT_ROWS = parseInt(__ENV.SLOW_CLIENT_ROWS || '8000', 10);

// ---------------------------------------------------------------------------
// k6 scenario definitions
// ---------------------------------------------------------------------------

export const options = {
  scenarios: {
    // Normal clients: constant arrival rate throughout all phases.
    // These measure degradation when slot-occupier VUs are active.
    normal_insert: {
      executor: 'constant-arrival-rate',
      rate: 300,
      timeUnit: '1s',
      preAllocatedVUs: 80,
      maxVUs: 120,
      duration: '15m',
      startTime: '0s',
      exec: 'normal_insert',
    },
    // Slot-occupier clients: fixed VU count active only during the pressure
    // phase. Each VU sends large batches back-to-back with no sleep, holding
    // an insert slot for as long as VMAgent takes to process the request.
    slot_occupier: {
      executor: 'constant-vus',
      vus: 14,
      duration: '10m',
      startTime: '2m',
      exec: 'slot_occupier',
    },
    // Lightweight read workload throughout all phases.
    read: {
      executor: 'constant-arrival-rate',
      rate: 50,
      timeUnit: '1s',
      preAllocatedVUs: 20,
      maxVUs: 40,
      duration: '15m',
      startTime: '0s',
      exec: 'read',
    },
  },
  insecureSkipTLSVerify: true,
};

// ---------------------------------------------------------------------------
// Remote-write clients
// ---------------------------------------------------------------------------

const normalClient = new remote.Client({ url: VMINSERT_URL });
const slowClient   = new remote.Client({ url: VMINSERT_URL });

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildNormalBatch returns a single-row timeseries for the normal workload.
function buildNormalBatch() {
  return [
    remote.Timeseries(
      {
        __name__: 'k6_normal_insert',
        job:       'k6_slow_client_test',
        namespace: VM_NAMESPACE,
        vu:        String(__VU),
      },
      [remote.Sample(__ITER, Date.now())],
    ),
  ];
}

// buildSlowBatch returns a large batch that forces VMAgent to spend more time
// parsing the request body, extending how long the insert slot is occupied.
function buildSlowBatch() {
  const batch = [];
  const ts = Date.now();
  for (let i = 0; i < SLOW_CLIENT_ROWS; i++) {
    batch.push(
      remote.Timeseries(
        {
          __name__:  `k6_slow_${i % 200}`,
          job:       'k6_slow_client_test',
          namespace: VM_NAMESPACE,
          vu:        String(__VU),
          idx:       String(i),
        },
        [remote.Sample(i + 1, ts)],
      ),
    );
  }
  return batch;
}

// ---------------------------------------------------------------------------
// Executors
// ---------------------------------------------------------------------------

export function normal_insert() {
  const res = normalClient.store(buildNormalBatch());
  check(res, {
    'normal insert status is 2xx': (r) => r.status >= 200 && r.status < 300,
  });
}

// Slot-occupier: sends large batches continuously with no sleep so the VU
// always has an in-flight request — i.e. always occupying one insert slot.
export function slot_occupier() {
  const res = slowClient.store(buildSlowBatch());
  check(res, {
    'slot-occupier insert status is 2xx': (r) => r.status >= 200 && r.status < 300,
  });
}

export function read() {
  const now   = Date.now();
  const start = Math.floor((now - 5 * 60 * 1000) / 1000);
  const end   = Math.floor(now / 1000);

  const res = http.post(
    VMSELECT_URL,
    {
      query: `sum(rate(k6_normal_insert{job="k6_slow_client_test",namespace="${VM_NAMESPACE}"}[1m]))`,
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
