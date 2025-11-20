import http from "k6/http";
import { check } from "k6";
import { randomIntBetween } from "https://jslib.k6.io/k6-utils/1.2.0/index.js";

export const options = {
  scenarios: {
    test_metric: {
      executor: "constant-vus",
      duration: "10m",
      exec: "test_metric",
      vus: 50,
    },
    test_sum: {
      executor: "constant-vus",
      duration: "10m",
      vus: 50,
      exec: "test_sum",
    },
    test_rate: {
      executor: "constant-vus",
      duration: "10m",
      vus: 50,
      exec: "test_rate",
    },
  },

  insecureSkipTLSVerify: true,
};

export function run_query(query) {
  let url =
    "http://vmselect-vmks.vm.svc.cluster.local.:8481/select/0/prometheus/api/v1/query_range";

  // Fetch last 15 mins data with 10% jitter
  const now_ns = Date.now() * 1_000_000; // Convert current time to nanoseconds
  const nominal_duration_ns = 15 * 60 * 1000 * 1_000_000; // 15 minutes in nanoseconds

  // Calculate the total jitter range (10% of the 15-minute nominal duration)
  const jitter_nanoseconds = Math.round(nominal_duration_ns * 0.1);

  // Calculate the nominal end time (current time) and apply jitter
  const end_nominal_ns = now_ns;
  const end_min_ns = end_nominal_ns - jitter_nanoseconds;
  const end_max_ns = end_nominal_ns + jitter_nanoseconds;
  let end = randomIntBetween(end_min_ns, end_max_ns);

  // Calculate the nominal start time (current time minus 15 minutes) and apply jitter
  const start_nominal_ns = now_ns - nominal_duration_ns;
  const start_min_ns = start_nominal_ns - jitter_nanoseconds;
  const start_max_ns = start_nominal_ns + jitter_nanoseconds;
  let start = randomIntBetween(start_min_ns, start_max_ns);

  let res = http.post(
    url,
    {
      query: query,
      start: start,
      end: end,
      step: "60s",
    },
    {
      // headers: { Authorization: "Bearer k6-secret-token" },
    },
  );
  check(res, {
    "status is 200": (r) => r.status === 200,
  });
}

export function test_sum() {
  run_query("sum by(le) (increase(vm_request_duration_seconds[1m]))");
}

export function test_metric() {
  run_query("up");
}

export function test_rate() {
  run_query("rate(avalanche_gauge_metric_mmmmm_0_0[5m])");
}
