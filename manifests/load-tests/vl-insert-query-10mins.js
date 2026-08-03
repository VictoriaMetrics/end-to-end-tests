import http from "k6/http";
import { check } from "k6";

const K6_DURATION = __ENV.SCENARIO_DURATION || "10m";
const INSERT_RATE = Number(__ENV.K6_INSERT_RATE || 500);
const READ_VUS = Number(__ENV.K6_READ_VUS || 20);
const BATCH_SIZE = Number(__ENV.K6_BATCH_SIZE || 10);
const PREALLOCATED_VUS = Number(__ENV.K6_PREALLOCATED_VUS || 50);
const MAX_VUS = Number(__ENV.K6_MAX_VUS || 100);
const STREAM_FIELDS = __ENV.VL_STREAM_FIELDS || "namespace,service,level";

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

const VL_INSERT_URL =
  __ENV.VL_INSERT_URL ||
  "http://vlinsert-vl-load-baseline.127.0.0.1.nip.io/insert/jsonline";
const VL_SELECT_URL =
  __ENV.VL_SELECT_URL ||
  "http://vlselect-vl-load-baseline.127.0.0.1.nip.io/select/logsql/query";
const VL_NAMESPACE = __ENV.VL_NAMESPACE || "vl-load-baseline";

const LEVELS = ["info", "warn", "error", "debug"];
const SERVICES = ["api-gateway", "auth-service", "data-processor", "cache-manager", "scheduler"];

function randomInt(min, max) {
  return Math.floor(Math.random() * (max - min + 1)) + min;
}

function randomFrom(arr) {
  return arr[randomInt(0, arr.length - 1)];
}

function buildLogLine(ts) {
  const level = randomFrom(LEVELS);
  const svc = randomFrom(SERVICES);
  const reqId = `req-${__VU}-${__ITER}-${randomInt(1000, 9999)}`;
  const dur = randomInt(1, 5000);
  const msg = `handled request ${reqId} in ${dur}ms status=${randomInt(200, 503)}`;
  return JSON.stringify({
    _time: ts,
    _msg: msg,
    level: level,
    service: svc,
    namespace: VL_NAMESPACE,
    request_id: reqId,
    stream_id: `stream-${randomInt(0, 9999)}`,
    duration_ms: dur,
  });
}

export function insert() {
  const now = new Date().toISOString();
  const lines = [];
  for (let i = 0; i < BATCH_SIZE; i++) {
    lines.push(buildLogLine(now));
  }
  const body = lines.join("\n");

  const res = http.post(
    `${VL_INSERT_URL}?_stream_fields=${encodeURIComponent(STREAM_FIELDS)}&_time_field=_time&_msg_field=_msg`,
    body,
    {
      headers: { "Content-Type": "application/stream+json" },
      responseType: "none",
    },
  );
  check(res, {
    "insert status is 2xx": (r) => r.status >= 200 && r.status < 300,
  });
}

export function read() {
  const svc = randomFrom(SERVICES);
  const query = `namespace:${VL_NAMESPACE} service:${svc}`;
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
