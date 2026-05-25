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

Public metadata for all registered nodes. Internal URLs and secrets are never included.

```json
[{"id": "node1", "name": "Tehran — ISP", "location": "Tehran", "isp": "ISP Name"}]
```

---

### GET /api/ping

SSE stream, single node. Params: `target` (required), `count` (1–20, default 5).

Each event carries one line of ping output. Stream ends with `data: [DONE]` or `data: [ERROR] <message>`.

---

### GET /api/ping-all

All nodes in parallel. Returns when all respond or time out.

Params: `target` (required).

```json
{
  "target": "1.1.1.1",
  "results": [
    {"id":"node1","name":"Tehran — ISP","isp":"ISP",
     "sent":4,"received":4,"loss":0,
     "rtt_min":0.7,"rtt_avg":0.8,"rtt_max":0.9,"status":"ok"}
  ]
}
```

Status: `ok`, `degraded` (partial loss), `down` (100% loss), `error` (unreachable).

---

### GET /api/traceroute

SSE stream, single node. Params: `target` (required), `maxhops` (5–64, default 30).

---

### GET /api/proxy

Proxies ping or traceroute SSE from a specific agent.

Params: `node` (required), `action` (`ping` or `traceroute`), `target`, and action-specific params.

---

### GET /api/portcheck

TCP port check via a specific agent. `http://` and `https://` prefixes are stripped from target automatically.

Params: `node`, `target`, `port` (1–65535) — all required.

```json
{"target":"example.ir","port":443,"status":"open","latency_ms":12}
```

Status: `open`, `closed` (refused), `filtered` (timeout).

---

### GET /api/dig

**Parameters:**
- `target` (required): Domain name
- `qtype`: Record type (`A`, `AAAA`, `MX`, `NS`, `TXT`, `CNAME`, `SOA`, `PTR`). Default: `A`
- `debug`: Set to `1` to see per-resolver status

**Behavior:**
Returns summarized results instead of raw `dig` output.  
Each record shows how many resolvers returned it.

**Example response lines:**
example.com. IN A 93.184.216.34   (found on 12 resolvers)
=== Summary ===
Record found on 12 out of 15 resolvers

---

### GET /api/ssl

TLS dial and certificate inspection. On validation failure, retries with `InsecureSkipVerify` and returns the certificate alongside the error.

Params: `target` — hostname or `host:port`, default port 443.

```json
{
  "valid": true,
  "subject": "*.example.ir",
  "issuer": "Let's Encrypt",
  "not_before": "2026-01-01 00:00:00 UTC",
  "not_after":  "2026-04-01 00:00:00 UTC",
  "days_left":  42,
  "sans": ["*.example.ir","example.ir"]
}
```

---

### GET /api/bgp

BGP route lookup enriched with GeoIP and AS operator names when a GeoIP database is loaded.

Params: `type` (`ip`, `prefix`, or `asn`), `query` (IP, CIDR, or ASN).

```json
{
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
  ]
}
```

`geo` and `aspath_enriched` present only for `type=ip` when GeoIP is loaded. ASN lookup capped at 1000 routes.

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

ping-summary response: `{"sent":4,"received":4,"loss":0,"rtt_min":0.7,"rtt_avg":0.8,"rtt_max":0.9}`

portcheck response: `{"target":"1.1.1.1","port":443,"status":"open","latency_ms":5}`