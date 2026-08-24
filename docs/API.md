# API Reference

## Master endpoints

All endpoints served by master on `LISTEN_ADDR` (default `127.0.0.1:8082`), proxied through nginx.

---

### GET /api/myip

Returns client IP, honouring `X-Forwarded-For` and `X-Real-IP`.

```json
{"ip": "1.2.3.4"}
```

---

### GET /api/info

BGP store statistics.

```json
{"route_count": 1374785, "bgp_updated": "2026-05-01 00:00 UTC"}
```

---

### GET /api/nodes

Public metadata for all currently-reachable nodes. Internal URLs and secrets are never included. A node that fails its background health check (see `docs/ARCHITECTURE.md` "Agent liveness") is omitted until it recovers — there is no status field marking it dead, it simply isn't in the list.

```json
[{"id": "node1", "name": "Tehran — ISP", "location": "Tehran", "isp": "ISP Name"}]
```

---

### GET /api/ping

SSE stream, single node. Params: `target` (required), `count` (1–20, default 5).

The first event is a named `request_id` event (`event: request_id`),
sent before any ping output, carrying the ID needed to promote this
result via `/api/report/promote`. Each following (default, unnamed) event
carries one line of ping output. Stream ends with `data: [DONE]` or
`data: [ERROR] <message>`.

---

### GET /api/ping-all

All currently-reachable nodes in parallel (see `docs/ARCHITECTURE.md` "Agent liveness") — a node that's been marked dead by the background health check gets no entry in `results` at all, not an `error` row. Returns when all respond or time out.

Params: `target` (required).

```json
{
  "target": "1.1.1.1",
  "results": [
    {"id":"node1","name":"Tehran — ISP","isp":"ISP",
     "sent":4,"received":4,"loss":0,
     "rtt_min":0.7,"rtt_avg":0.8,"rtt_max":0.9,"status":"ok"}
  ],
  "request_id": "6e6f7f0263404ec8d7e06885af9290b7ba128b1a"
}
```

Status: `ok`, `degraded` (partial loss), `down` (100% loss), `error` (unreachable).

`request_id` promotes this result via `/api/report/promote` (`kind: "ping-all"`).

---

### GET /api/http-check

All currently-reachable nodes in parallel, same fan-out convention as `/api/ping-all`. Returns when all respond or time out. Does not touch the subprocess semaphores — this is a `net/http` call on each agent, not `exec.Command`.

Params: `target` (required) — a URL including scheme; only `http` and `https` are accepted.

Issues a `GET` to `target` from each node with redirects **not** followed (a 301 is reported as a 301) and TLS certificate verification enabled (an invalid cert is a reportable failure, not bypassed).

```json
{
  "target": "https://example.com",
  "results": [
    {"id":"node1","name":"node1",
     "status":"ok","status_code":200,"reason":"OK",
     "elapsed_ms":123.4,"ip":"5.6.7.8"}
  ],
  "request_id": "6e6f7f0263404ec8d7e06885af9290b7ba128b1a"
}
```

`status` is `ok` (a response was received, whatever its status code) or `error` (the request itself failed). On `error`, `error` carries a classification instead of a raw Go error string: `timeout`, `connection_refused`, `dns_error`, `tls_error`, `connection_failed`, or `invalid_target`.

`request_id` promotes this result via `/api/report/promote` (`kind: "http-check"`).

---

### GET /api/traceroute

SSE stream, single node. Params: `target` (required), `maxhops` (5–64, default 30).

Same `request_id` event convention as `/api/ping`: a named `request_id`
event first, before any hop line.

---

### GET /api/proxy

Proxies ping, traceroute, or portcheck from a specific agent.

Params: `node` (required), `action` (`ping`, `traceroute`, or `portcheck`), `target`, and one action-specific param: `count` for ping, `maxhops` for traceroute, `port` for portcheck.

For `action=ping` and `action=traceroute`, the same `request_id` SSE event
convention applies (first event, before any hop line), and the result is
promotable the same way as the non-proxied endpoints above. `action=portcheck`
through this endpoint does **not** get a `request_id` — see
`/api/portcheck` below for the portcheck path that does.

---

### GET /api/portcheck

TCP port check via a specific agent. `http://` and `https://` prefixes are stripped from target automatically.

Params: `node`, `target`, `port` (1–65535) — all required.

```json
{"target":"example.ir","port":443,"status":"open","latency_ms":12,"request_id":"..."}
```

Status: `open`, `closed` (refused), `filtered` (timeout).

`request_id` promotes this result via `/api/report/promote` (`kind:
"portcheck"`). Omitted if the agent's response couldn't be parsed as JSON.

---

### GET /api/dig

**Parameters:**
- `target` (required): Domain name
- `qtype`: Record type (`A`, `AAAA`, `MX`, `NS`, `TXT`, `CNAME`, `SOA`, `PTR`). Default: `A`
- `debug`: Set to `1` to see per-resolver status

**Behavior:**
Returns summarized results instead of raw `dig` output.  
Each record shows how many resolvers returned it.

The first event is a named `request_id` event, sent before any record
line, promotable via `/api/report/promote` (`kind: "dns"`). Only the
summarized record/summary lines are captured for promotion — per-resolver
`debug=1` status lines are not part of the stored result.

**Example response lines:**
example.com. IN A 93.184.216.34   (found on 12 resolvers)
=== Summary ===
Record found on 12 out of 15 resolvers

---

### GET /api/ssl

TLS dial and certificate inspection. On validation failure, retries with `InsecureSkipVerify` and returns the certificate alongside the error.

Params: `target` — hostname, IP, host:port, or ip:port (default port 443).

```json
{
  "valid": true,
  "subject": "*.example.ir",
  "issuer": "Let's Encrypt",
  "not_before": "2026-01-01 00:00:00 UTC",
  "not_after":  "2026-04-01 00:00:00 UTC",
  "days_left":  42,
  "sans": ["*.example.ir","example.ir"],
  "request_id": "a37ec32ba3ea8ae2b302ade9ee0d44770c77cf14"
}
```

`request_id` promotes this result via `/api/report/promote` (`kind: "ssl"`).

---

### GET /api/bgp

BGP route lookup enriched with GeoIP and AS operator names when a GeoIP database is loaded.

Params: `type` (`ip`, `prefix`, or `asn`), `query` (IP, CIDR, or ASN).

```json
{
  "type": "ip",
  "count": 1,
  "routes": [
    {
      "prefix": "8.8.8.0/24",
      "aspath": [34549,15169],
      "origin": "igp",
      "localpref": 0, "med": 0,
      "communities": ["34549:9999"],
      "geo": {
        "country":"United States","country_code":"US",
        "continent":"North America",
        "asn":"AS15169","as_name":"Google LLC","as_domain":"google.com"
      }
    }
  ],
  "aspath_enriched": [
    {"asn":34549,"name":"meerfarbig GmbH & Co. KG","domain":"meerfarbig.net"},
    {"asn":15169,"name":"Google LLC","domain":"google.com"}
  ],
  "request_id": "82621ff66c895fc6d5f0be4137024217e4db5535"
}
```

`geo` and `aspath_enriched` present only for `type=ip` when GeoIP is loaded. ASN lookup capped at 1000 routes. `type` echoes the normalized (lowercased) lookup type. `request_id` promotes this result via `/api/report/promote` (`kind: "bgp"`).

---

### GET /api/ip-info

Batch GeoIP/AS lookup, used by the UI to enrich traceroute hop IPs.

Params: `targets` (required, comma-separated, capped at 50 per request).

```json
{"1.1.1.1": {"asn": "AS13335", "name": "Cloudflare, Inc.", "domain": "cloudflare.com"}}
```

Returns `{}` if no GeoIP database is loaded.

---

### POST /api/report/promote

Turns a still-live ephemeral check result into a permanent, disk-backed
report — the server side of the "Permanent Link" feature. See
`docs/ARCHITECTURE.md` ("Permanent Link reports") for the ephemeral-cache/
persisted-store design behind this.

Body (JSON, capped at 4096 bytes):

```json
{"request_id": "9d2955511297001b4274dbeb86efe3f467b3c486"}
```

`request_id` is the value returned by a prior check — the `request_id`
field on a JSON response, or the initial `request_id` SSE event on a
streaming one.

Success:

```json
{"id": "3ca708fe93e0ae6495e168ceb8638e59ae429492"}
```

Gated by a dedicated per-IP limiter — 10 requests/hour, burst 3 —
independent of the general token bucket every other endpoint shares.

| Code | Body | Trigger |
|---|---|---|
| 400 | `{"error":"invalid request body"}` | body is not valid JSON |
| 400 | `{"error":"invalid request id"}` | `request_id` is not a 40-character lowercase-hex string |
| 429 | `{"error":"too many permanent links requested — try again later"}` | promote's own 10/hour-per-IP limit reached |
| 404 | `{"error":"this result is no longer available to make permanent — please re-run the check"}` | `request_id` is unknown, or its 30-minute ephemeral window has passed |
| 503 | `{"error":"too many active shared links right now — please try again later"}` | 2000 reports are already active on disk (rejected outright, nothing is evicted) |
| 500 | `{"error":"failed to save permanent link"}` | writing the report to disk failed |
| 503 | `{"error":"permanent links are unavailable"}` | `REPORTS_DIR` failed to initialize at startup |

---

### GET /api/report

Fetches a promoted report by ID for rendering. Public and unauthenticated
by design — the same trust model as any other shareable link.

Params: `id` (required).

```json
{
  "id": "3ca708fe93e0ae6495e168ceb8638e59ae429492",
  "kind": "bgp",
  "target": "12880",
  "captured_at": "2026-08-05T02:33:21Z",
  "data": { "...": "shape depends on kind, see below" }
}
```

`kind` is one of `ping`, `ping-all`, `traceroute`, `dns`, `portcheck`,
`ssl`, `bgp`, `http-check` — whichever check produced the report.
`captured_at` is RFC 3339, always UTC.

`data`'s shape depends on `kind`:

- `ping-all`, `portcheck`, `ssl`, `bgp`, `http-check` — the same JSON body
  that check's own live endpoint returns, minus its own `request_id`
  field.
- `ping`, `traceroute`, `dns` — these are SSE endpoints live, so there's no
  JSON body to mirror; `data` is instead `{"target": "...", "lines":
  ["...", ...], ...}`, the transcript captured while streaming, plus the
  extra param each carried (`count` for ping, `maxhops` for traceroute,
  `qtype` for dns).

| Code | Body | Trigger |
|---|---|---|
| 404 | `{"error":"report not found or expired"}` | `id` is malformed, unknown, or past its 24-hour window |
| 429 | `{"error":"rate limited — try again in a minute"}` | the general per-IP token bucket is exhausted (same limiter and message as every other endpoint) |
| 503 | `{"error":"permanent links are unavailable"}` | `REPORTS_DIR` failed to initialize at startup |

---

## Agent endpoints

All require `X-Agent-Secret` header.

| Path | Description |
|---|---|
| GET `/health` | Returns `ok` |
| GET `/ping` | SSE stream. Params: `target`, `count` |
| GET `/traceroute` | SSE stream. Params: `target`, `maxhops` |
| GET `/ping-summary` | Parsed ping JSON. Params: `target`, `count` (default 4) |
| GET `/portcheck` | Port check JSON. Params: `target`, `port` |
| GET `/http-check` | HTTP(S) connectivity check JSON. Params: `target` (full URL) |

ping-summary response: `{"sent":4,"received":4,"loss":0,"rtt_min":0.7,"rtt_avg":0.8,"rtt_max":0.9}`

portcheck response: `{"target":"1.1.1.1","port":443,"status":"open","latency_ms":5}`

http-check response, success: `{"target":"https://example.ir","status_code":200,"reason":"OK","elapsed_ms":123.4,"ip":"5.6.7.8"}`

http-check response, failure: `{"target":"https://example.ir","error":"timeout"}` — `error` is one of `timeout`, `connection_refused`, `dns_error`, `tls_error`, `connection_failed`, `invalid_target`. Redirects are never followed — the first response's status is reported as-is. 10s total budget, same class as `/portcheck`.