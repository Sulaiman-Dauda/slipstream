// Slipstream benchmark: WordPress under load.
//
//   k6 run -e TARGET=https://bench.example.com bench/k6/wordpress.js
//
// Scenarios:
//   cached    — anonymous homepage traffic (page cache hot path)
//   uncached  — cache-busting query strings (full PHP + DB path)
//   spike     — sudden 10x burst against one URL (coalescing test)
//
// Compare the same script against CloudPanel's recommended WordPress
// configuration on identical hardware. Publish everything: this is the
// evidence behind the performance claim.
import http from "k6/http";
import { check } from "k6";
import { Trend, Rate } from "k6/metrics";

const TARGET = __ENV.TARGET;
if (!TARGET) throw new Error("set -e TARGET=https://your-bench-site");

const cacheHit = new Rate("slipstream_cache_hit");
const uncachedLatency = new Trend("uncached_latency", true);

export const options = {
  insecureSkipTLSVerify: true,
  scenarios: {
    cached: {
      executor: "constant-vus",
      vus: 50,
      duration: "2m",
      exec: "cachedTraffic",
    },
    uncached: {
      executor: "constant-vus",
      vus: 10,
      duration: "2m",
      exec: "uncachedTraffic",
      startTime: "2m10s",
    },
    spike: {
      executor: "ramping-vus",
      startVUs: 5,
      stages: [
        { duration: "20s", target: 5 },
        { duration: "10s", target: 200 }, // the burst
        { duration: "30s", target: 200 },
        { duration: "20s", target: 5 },
      ],
      exec: "spikeTraffic",
      startTime: "4m30s",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    "http_req_duration{scenario:cached}": ["p(95)<100"],
    "http_req_duration{scenario:uncached}": ["p(95)<1500"],
  },
};

const PATHS = ["/", "/?p=1", "/sample-page/"];

export function cachedTraffic() {
  const res = http.get(TARGET + PATHS[Math.floor(Math.random() * PATHS.length)].split("?")[0]);
  check(res, { "status 200": (r) => r.status === 200 });
  const cache = res.headers["X-Slipstream-Cache"];
  if (cache !== undefined) cacheHit.add(cache === "HIT" || cache === "STALE" || cache === "UPDATING");
}

export function uncachedTraffic() {
  // Unique query strings always bypass the page cache: this measures the
  // real PHP + MariaDB path.
  const res = http.get(`${TARGET}/?bench=${__VU}-${__ITER}-${Date.now()}`);
  check(res, { "status 200": (r) => r.status === 200 });
  uncachedLatency.add(res.timings.duration);
}

export function spikeTraffic() {
  // Everyone hammers the same URL. With request coalescing one request
  // regenerates while the rest are served from cache/stale — origin load
  // should stay flat.
  const res = http.get(TARGET + "/");
  check(res, { "spike survived": (r) => r.status === 200 });
}
