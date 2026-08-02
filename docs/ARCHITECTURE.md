# Architecture

## Overview

Two binaries. The **master** serves the UI, holds the BGP table, and proxies probe requests to agents. The **agent** runs on every measurement node and executes network operations.

```
                     ┌──────────────────────────────┐
                     │          Master Node          │
      User ─HTTPS──▶ │  nginx → looking-glass :8082  │
                     │  agent (127.0.0.1:9090)       │
                     └──────────┬────────────────────┘
                                │  HTTP + X-Agent-Secret
               ┌────────────────┼────────────────┐
               ▼                ▼                ▼
           Node A            Node B           Node C
          agent:9090        agent:9090       agent:9090
```

The master and agents communicate over private networking. The agent port is never exposed to the public internet.

---

## BGP table

Routes are loaded from a JSON file converted from MRT TABLE_DUMP2 format. The file is read once at startup and again whenever its mtime changes (polled every 5 minutes). During reload, the old snapshot continues serving requests until the new one is fully parsed. The swap is atomic via `sync/atomic.Pointer`.

Prefix lookup uses a binary radix trie — one trie for IPv4, one for IPv6. Each node in the trie holds a slice of routes. IP lookup walks the trie bit by bit and returns the deepest matching node (longest prefix match). Prefix lookup walks exactly `prefix_length` bits and returns routes at that node only.

ASN lookup is a linear scan of an inverted index built at load time: a `map[int][]Route` keyed by ASN. Results are capped at 1000 to prevent excessive memory allocation in responses.

Memory: a full global BGP table (~1.4M prefixes) occupies approximately 2 GB RSS.

---

## GeoIP

The ipinfo Lite CSV is loaded into the same binary radix trie structure as BGP routes. A hash map (`map[string]*Record`) is built alongside it in a single pass, keyed by ASN string (`AS15169`), for O(1) operator name resolution during BGP response enrichment.

MaxMind MMDB format was evaluated but not adopted. A pure Go MMDB reader was implemented and correctly parsed metadata and traversed the trie, but triggered goroutine stack overflow on deeply nested pointer chains in the data section. The ipinfo CSV is simpler, requires no custom binary format parser, and uses the same trie infrastructure already present in the codebase.

---

## Probe streaming

Ping (single-node mode) and traceroute output is streamed line-by-line via Server-Sent Events. Each line is written as a `data:` field and flushed immediately. The `X-Accel-Buffering: no` response header disables nginx proxy buffering for SSE responses.

WebSocket was evaluated and rejected: it requires a connection upgrade, adds bidirectional framing overhead, and is harder to proxy correctly through nginx without additional configuration.

---

## Multi-node ping

The `/api/ping-all` endpoint fans out to all registered agents in parallel using goroutines, bounded to 8 concurrent agent requests at a time. Each agent executes `ping -c 4`, parses the output, and returns structured JSON (`sent`, `received`, `loss`, `rtt_min`, `rtt_avg`, `rtt_max`). The master waits for all goroutines to complete and returns a single JSON response. The UI renders results as a table that populates when the response arrives.

The original ping implementation streamed output from a single selected node via SSE, mirroring traceroute. This was replaced because the parallel table view is more useful for network diagnostics — it shows relative performance across ISPs in a single request.

---

## Rate limiting

Three layers:

**nginx** — `limit_req_zone` at 20 req/s (general) and 6 req/min (`/api/`). Configured in `nginx.conf` and per-vhost. Returns 429 on breach.

**Application token bucket** — per IP, 20 req/min sustained, burst of 5. In-process, no Redis. Implemented in `internal/ratelimit`. Entries are cleaned up after 30 minutes of inactivity.

**Per-IP subprocess semaphore** — each IP may hold at most one active subprocess (ping, traceroute, dig) at a time, or one active request via `/api/proxy` or `/api/portcheck`. Implemented via a `sync.Map` of buffered `chan struct{}` with capacity 1, cleaned up after 30 minutes of inactivity. Prevents a single IP from holding multiple long-running processes simultaneously.

A global semaphore (`chan struct{}` with capacity 30) bounds total concurrent subprocesses across all IPs, and the same semaphore gates `/api/proxy` and `/api/portcheck`.

`/api/bgp` is gated by the application token bucket like every other target-facing endpoint. `/api/myip`, `/api/info`, and `/api/nodes` are deliberately left unthrottled — they're called on every page load, are cheap in-memory lookups, and rate limiting them risks breaking legitimate usage for shared/NAT IPs for negligible security benefit.

---

## Input validation

All user-supplied targets pass through `internal/validator` before reaching any subprocess. The validator accepts valid IPv4/IPv6 addresses and RFC-compliant hostnames. It rejects inputs containing shell metacharacters. `exec.Command` is called with arguments as a slice — no shell interpolation occurs at any point.

`internal/validator`'s `ValidateNotPrivate` additionally rejects any target that is, or resolves via DNS to, a loopback, private (RFC 1918/4193), link-local, unspecified, or multicast address — including `169.254.169.254`, the common cloud-metadata endpoint. It does not run on `/api/bgp`'s `ip` lookup, since that only queries the local BGP/GeoIP tries and never opens a connection to the target, nor on `/api/dig`, since `dig` only asks the fixed list of public resolvers (`h.resolvers`) a DNS question about the target name — it never connects to the target address itself, so a private-range target there is not an SSRF vector, and rejecting it would break legitimate reverse-DNS (PTR) lookups against internal IPs. A DNS lookup failure is treated as non-blocking — resolution errors are left for the downstream probe to report.

`ValidateNotPrivate` returns the resolved IP alongside the validation result. For the endpoints that connect to the target directly from the master — `ping`, `traceroute`, `ssl` — that IP is pinned and reused for the actual `ping`/`traceroute` subprocess or `tls.DialWithDialer` call instead of re-resolving the hostname a second time. Without this, a DNS-rebinding attacker could return a public address at validation time and a private one moments later when the real connection is made; pinning closes that window since there's only ever one resolution. `SSLCheck` dials the pinned IP but keeps the original hostname as `tls.Config.ServerName`, so SNI and certificate hostname matching are unaffected. One observable side effect: `ping`/`traceroute`'s own output header now shows the resolved IP rather than the original hostname when a hostname target was given — expected, not a bug.

`proxy`, `portcheck`, and `ping-all` also call `ValidateNotPrivate`, but discard the returned IP and forward the original target string to the agent. This is a master-side check only: it rejects literal private-IP targets and whatever a single DNS resolution says at that instant, but it does not close the rebinding window for these three endpoints, because the actual ping/traceroute/portcheck runs on the agent's host, which independently resolves whatever string it receives. Closing that fully requires the same pinning logic inside `cmd/agent`, which isn't present in this checkout — tracked as separate future work, not implemented here.

`Proxy` and `PortCheck` return a fixed `"agent unreachable"` message on agent-connection failure, with none of the underlying error text included — there is nothing in that response to sanitize or leak, regardless of what shape the underlying network error takes.

`SSLCheck` separately uses a `sanitizeErr` helper that strips `dial tcp <src>->` and similar prefixes from error strings. This only rewrites errors that contain that `->` pattern (established-connection read/write failures); it does not rewrite `dial tcp <addr>: connect: connection refused` (connection-never-established failures), which is the common case when a target simply isn't listening. That gap is a known limitation, not yet fixed — it's lower severity than the `Proxy`/`PortCheck` case because the address exposed there is the user's own requested target, not an internal secret.

---

## Security model

- Agent endpoints require `X-Agent-Secret` header. The secret is a 32-byte random hex string shared across all nodes.
- Agent port 9090 is restricted to the master IP via ufw on each agent node.
- The agent URL and secret are never returned to clients. The `/api/nodes` endpoint returns only public metadata (ID, name, ISP).
- The master binary is deployed behind nginx. It binds `127.0.0.1:8082` and is not directly reachable from the internet.
- Client IP for rate limiting is taken from `X-Real-IP` first, falling back to `X-Forwarded-For` only if absent — `X-Real-IP` is set by nginx from `$remote_addr` and cannot be overridden by the client, whereas `X-Forwarded-For` can be prefixed with attacker-controlled values by nginx configs that append rather than overwrite it.