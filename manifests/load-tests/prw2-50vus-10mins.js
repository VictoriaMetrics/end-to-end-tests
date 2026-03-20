import http from "k6/http";
import { check } from "k6";
import { randomIntBetween } from "https://jslib.k6.io/k6-utils/1.2.0/index.js";

export const options = {
  scenarios: {
    insert: {
      executor: "constant-vus",
      duration: "10m",
      vus: 50,
      exec: "insert",
    },
    read: {
      executor: "constant-vus",
      duration: "10m",
      vus: 50,
      exec: "read",
    },
  },
  insecureSkipTLSVerify: true,
};

const VMSELECT_URL = "http://vmselect-vmks.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range";
const VMINSERT_URL = "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/write";

// ---- Minimal protobuf encoder for io.prometheus.write.v2.Request ----
//
// Request  { 1: string[] symbols, 2: TimeSeries[] timeseries }
// TimeSeries { 1: uint32[] labels_refs (packed), 2: Sample[] samples }
// Sample   { 1: double value, 2: int64 timestamp (ms) }

function varint(n) {
  const out = [];
  while (n > 127) {
    out.push((n & 0x7f) | 0x80);
    n = Math.floor(n / 128);
  }
  out.push(n & 0x7f);
  return out;
}

function lenField(fieldNum, data) {
  return [...varint((fieldNum << 3) | 2), ...varint(data.length), ...data];
}

function stringField(fieldNum, str) {
  const bytes = [];
  for (let i = 0; i < str.length; i++) bytes.push(str.charCodeAt(i) & 0xff);
  return lenField(fieldNum, bytes);
}

function packedUint32s(fieldNum, values) {
  const inner = [];
  for (const v of values) inner.push(...varint(v));
  return lenField(fieldNum, inner);
}

function doubleField(fieldNum, value) {
  const buf = new ArrayBuffer(8);
  new DataView(buf).setFloat64(0, value, true); // little-endian IEEE 754
  return [...varint((fieldNum << 3) | 1), ...new Uint8Array(buf)];
}

function varintField(fieldNum, n) {
  return [...varint((fieldNum << 3) | 0), ...varint(n)];
}

// buildRequest constructs a PRW v2 Request containing one TimeSeries with one Sample.
// metricName and extraLabels populate the symbol table; value and timestampMs are the
// sample data. Returns an ArrayBuffer ready to POST.
function buildRequest(metricName, extraLabels, value, timestampMs) {
  // Symbol table: flat list of [name, value, name, value, ...]
  // __name__ is always first so it is sorted first by label name convention.
  const symbols = ["__name__", metricName];
  for (const [k, v] of Object.entries(extraLabels)) {
    symbols.push(k, v);
  }

  // labels_refs = [0, 1, 2, 3, ...] — sequential index pairs into symbols
  const labelsRefs = symbols.map((_, i) => i);

  // Sample: { value (field 1, double), timestamp (field 2, varint int64) }
  const sample = [...doubleField(1, value), ...varintField(2, timestampMs)];

  // TimeSeries: { labels_refs (field 1, packed uint32), samples (field 2, message) }
  const ts = [...packedUint32s(1, labelsRefs), ...lenField(2, sample)];

  // Request: repeated symbols (field 1), repeated timeseries (field 2)
  const req = [];
  for (const sym of symbols) req.push(...stringField(1, sym));
  req.push(...lenField(2, ts));

  return new Uint8Array(req).buffer;
}

function run_query(query) {
  const now = Date.now();
  const start = Math.floor((now - 10 * 60 * 1000) / 1000); // 10 min ago, seconds
  const end = Math.floor(now / 1000);

  const res = http.post(VMSELECT_URL, { query: query, start: start, end: end, step: "15s" }, {});
  check(res, {
    "status is 200": (r) => r.status === 200,
  });
}

export function read() {
  const metricIdx = randomIntBetween(0, 9);
  run_query(`k6_metric_${metricIdx}{job="k6_load_test"}`);
}

export function insert() {
  const metricIdx = randomIntBetween(0, 9);
  const payload = buildRequest(
    `k6_metric_${metricIdx}`,
    { instance: `vu-${__VU}`, job: "k6_load_test" },
    randomIntBetween(1, 10000),
    Date.now(),
  );

  const res = http.post(VMINSERT_URL, payload, {
    headers: {
      "Content-Type": "application/x-protobuf",
      "X-Prometheus-Remote-Write-Version": "2.0.0",
    },
  });
  check(res, {
    "status is 204": (r) => r.status === 204,
  });
}
