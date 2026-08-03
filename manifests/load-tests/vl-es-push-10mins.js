import http from "k6/http";
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";
const INSERT_RATE = Number(__ENV.K6_INSERT_RATE || 300);
const READ_VUS = Number(__ENV.K6_READ_VUS || 10);
const BATCH_SIZE = Number(__ENV.K6_BATCH_SIZE || 5);
const PREALLOCATED_VUS = Number(__ENV.K6_PREALLOCATED_VUS || 50);
const MAX_VUS = Number(__ENV.K6_MAX_VUS || 100);

export const options = {
  scenarios: {
    insert: {
      executor: "constant-arrival-rate",
      duration: K6_DURATION,
      rate: INSERT_RATE,
      timeUnit: "1s",
      preAllocatedVUs: PREALLOCATED_VUS,
      maxVUs: MAX_VUS,
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

// VL_INSERT_URL points at vlinsert base; we POST to /insert/elasticsearch/_bulk.
const VL_INSERT_BASE =
  __ENV.VL_INSERT_URL ||
  "http://vlinsert-vl-load-es.127.0.0.1.nip.io/insert/jsonline";
// Strip any path suffix to get the base URL for ES bulk endpoint.
const VL_ES_BASE = VL_INSERT_BASE.replace(/\/insert\/.*$/, "");
const ES_BULK_URL = `${VL_ES_BASE}/insert/elasticsearch/_bulk`;

const VL_SELECT_URL =
  __ENV.VL_SELECT_URL ||
  "http://vlselect-vl-load-es.127.0.0.1.nip.io/select/logsql/query";
const VL_NAMESPACE = __ENV.VL_NAMESPACE || "vl-load-es";

const LEVELS = ["INFO", "WARN", "ERROR", "DEBUG"];
const SERVICES = ["api-gateway", "auth-service", "data-processor", "cache-manager", "scheduler"];

function randomInt(min, max) {
  return Math.floor(Math.random() * (max - min + 1)) + min;
}

function randomFrom(arr) {
  return arr[randomInt(0, arr.length - 1)];
}

// Elasticsearch bulk format: alternating action + source lines (NDJSON).
// VL uses _index as the stream identifier by default.
function buildESBulkBody() {
  const svc = randomFrom(SERVICES);
  const lines = [];

  for (let i = 0; i < BATCH_SIZE; i++) {
    const level = randomFrom(LEVELS);
    const reqId = `req-${__VU}-${__ITER}-${randomInt(1000, 9999)}`;
    const dur = randomInt(1, 5000);
    const msg = `handled request ${reqId} in ${dur}ms status=${randomInt(200, 503)}`;
    const ts = new Date().toISOString();

    // Action line
    lines.push(JSON.stringify({ index: { _index: `${VL_NAMESPACE}.${svc}` } }));
    // Source line
    lines.push(JSON.stringify({
      "@timestamp": ts,
      level: level,
      message: msg,
      namespace: VL_NAMESPACE,
      "service.name": svc,
      request_id: reqId,
      duration_ms: dur,
    }));
  }

  // Bulk body must end with a newline
  return lines.join("\n") + "\n";
}

export function insert() {
  const body = buildESBulkBody();
  const res = http.post(ES_BULK_URL, body, {
    headers: {
      "Content-Type": "application/json",
    },
    responseType: "text",
  });
  check(res, {
    "es bulk insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export function read() {
  const svc = randomFrom(SERVICES);
  const query = `namespace:${VL_NAMESPACE} service.name:${svc}`;
  const limit = 100;

  const res = http.post(
    VL_SELECT_URL,
    `query=${encodeURIComponent(query)}&limit=${limit}&start=1h`,
    {
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      responseType: "none",
    },
  );
  check(res, {
    "query status is 200": (r) => r.status === 200,
  });
}

export default function () {}
