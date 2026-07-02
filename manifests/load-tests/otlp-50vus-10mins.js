import faker from 'k6/x/faker';
import http from "k6/http";
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";

export const options = {
  scenarios: {
    insert: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 500,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 150,
      exec: "insert",
    },
    read: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: 200,
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
const VMINSERT_OTLP_URL =
  __ENV.VMINSERT_OTLP_URL ||
  "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/opentelemetry/v1/metrics";
const VM_NAMESPACE = __ENV.VM_NAMESPACE || "monitoring";

// Protobuf encoding helpers
// Encodes a varint into a Uint8Array
function encodeVarint(value) {
  const bytes = [];
  while (value > 0x7f) {
    bytes.push((value & 0x7f) | 0x80);
    value = value >>> 7;
  }
  bytes.push(value & 0x7f);
  return new Uint8Array(bytes);
}

// Encodes a protobuf field with wire type 2 (length-delimited) and its payload
function encodeLenDelim(fieldNumber, payload) {
  const tag = encodeVarint((fieldNumber << 3) | 2);
  const len = encodeVarint(payload.length);
  const result = new Uint8Array(tag.length + len.length + payload.length);
  result.set(tag, 0);
  result.set(len, tag.length);
  result.set(payload, tag.length + len.length);
  return result;
}

// Encodes a fixed64 field (wire type 1) — used for start_time_unix_nano / time_unix_nano
function encodeFixed64Field(fieldNumber, value) {
  const tag = encodeVarint((fieldNumber << 3) | 1);
  // value is a JS number; use low/high 32-bit split (ns timestamps fit in 64 bits)
  const lo = value >>> 0;
  const hi = Math.floor(value / 4294967296) >>> 0;
  const result = new Uint8Array(tag.length + 8);
  result.set(tag, 0);
  result[tag.length + 0] = lo & 0xff;
  result[tag.length + 1] = (lo >>> 8) & 0xff;
  result[tag.length + 2] = (lo >>> 16) & 0xff;
  result[tag.length + 3] = (lo >>> 24) & 0xff;
  result[tag.length + 4] = hi & 0xff;
  result[tag.length + 5] = (hi >>> 8) & 0xff;
  result[tag.length + 6] = (hi >>> 16) & 0xff;
  result[tag.length + 7] = (hi >>> 24) & 0xff;
  return result;
}

// Encodes a UTF-8 string as bytes
function encodeString(s) {
  const bytes = [];
  for (let i = 0; i < s.length; i++) {
    const code = s.charCodeAt(i);
    if (code < 0x80) {
      bytes.push(code);
    } else if (code < 0x800) {
      bytes.push(0xc0 | (code >> 6));
      bytes.push(0x80 | (code & 0x3f));
    } else {
      bytes.push(0xe0 | (code >> 12));
      bytes.push(0x80 | ((code >> 6) & 0x3f));
      bytes.push(0x80 | (code & 0x3f));
    }
  }
  return new Uint8Array(bytes);
}

// Concatenates multiple Uint8Arrays
function concat(...arrays) {
  const total = arrays.reduce((s, a) => s + a.length, 0);
  const result = new Uint8Array(total);
  let offset = 0;
  for (const a of arrays) {
    result.set(a, offset);
    offset += a.length;
  }
  return result;
}

// Encodes a single KeyValue { key [1]: k, value [2]: AnyValue { string_value [1]: v } }
function encodeKeyValue(k, v) {
  const anyValue = encodeLenDelim(1, encodeString(v));
  return concat(encodeLenDelim(1, encodeString(k)), encodeLenDelim(2, anyValue));
}

function buildOTLPPayload(metricName, attrs, value) {
  const nowNs = Date.now() * 1_000_000;

  // Encode each attribute as a repeated attributes [7] field in NumberDataPoint
  const attrFields = attrs.map(([k, v]) => encodeLenDelim(7, encodeKeyValue(k, v)));

  // NumberDataPoint {
  //   attributes [7]: KeyValue (repeated)
  //   start_time_unix_nano [2]: fixed64
  //   time_unix_nano [3]: fixed64
  //   as_int [6]: sfixed64 (wire type 1, 8 bytes)
  // }
  const dataPoint = concat(
    ...attrFields,
    encodeFixed64Field(2, nowNs),
    encodeFixed64Field(3, nowNs),
    encodeFixed64Field(6, value),
  );

  // Gauge { data_points [1]: NumberDataPoint }
  const gauge = encodeLenDelim(1, dataPoint);

  // Metric { name [1]: metricName, gauge [5]: Gauge }
  const metric = concat(encodeLenDelim(1, encodeString(metricName)), encodeLenDelim(5, gauge));

  // ScopeMetrics { metrics [2]: Metric }
  const scopeMetrics = encodeLenDelim(2, metric);

  // ResourceMetrics { scope_metrics [2]: ScopeMetrics }
  const resourceMetrics = encodeLenDelim(2, scopeMetrics);

  // ExportMetricsServiceRequest { resource_metrics [1]: ResourceMetrics }
  return encodeLenDelim(1, resourceMetrics);
}

function run_query(query) {
  const now = Date.now();
  const start = Math.floor((now - 10 * 60 * 1000) / 1000);
  const end = Math.floor(now / 1000);
  const res = http.post(VMSELECT_URL, { query, start, end, step: "15s" }, { responseType: "none" });
  check(res, { "query status is 200": (r) => r.status === 200 });
}

export function read() {
  const metricIdx = faker.numbers.intRange(0, 9);
  run_query(
    `sum by(first_name, last_name) (rate(k6_otlp_metric_${metricIdx}{job="k6_load_test",namespace="${VM_NAMESPACE}"}[5m]))`,
  );
}

export function insert() {
  const metricIdx = faker.numbers.intRange(0, 9);
  const value = faker.numbers.intRange(1, 10000);
  const payload = buildOTLPPayload(
    `k6_otlp_metric_${metricIdx}`,
    [
      ["job", "k6_load_test"],
      ["namespace", VM_NAMESPACE],
      ["first_name", faker.person.firstName()],
      ["last_name", faker.person.lastName()],
    ],
    value,
  );
  const res = http.post(VMINSERT_OTLP_URL, payload.buffer, {
    headers: { "Content-Type": "application/x-protobuf" },
    responseType: "none",
  });
  check(res, { "insert status is 200": (r) => r.status == 200 });
}

export default function () {}
