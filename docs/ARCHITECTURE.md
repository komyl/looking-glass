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

## Permanent Link reports

Two independent storage layers back this feature, not one, because they answer different questions and need different failure behavior at capacity.

The **ephemeral cache** (`internal/report.EphemeralCache`) holds the actual result of every completed check — ping, ping-all, traceroute, portcheck, dns, ssl, bgp — for exactly 30 minutes, in an in-memory map, single-node/in-process only. There are three independent master/observer nodes and no state is synchronized between them: a promote request against node B for an ID minted by node A simply 404s. Every check writes into this cache once its result is fully known, whether or not anyone ever asks to keep it — a `request_id` JSON field for request/response endpoints, an initial named `request_id` SSE event (sent before any hop/result data, since a streaming client needs it up front) for the streaming ones. This happens unconditionally, independent of `REPORTS_DIR`: the disk layer below can be entirely unavailable and every check still gets a `request_id`.

Neither the per-IP subprocess semaphore nor the global 30-slot semaphore bounds how large this cache can grow. `BGP`, `SSLCheck`, and `PingAll` never touch either semaphore at all — `BGP` is a pure trie lookup, `SSLCheck` a direct `tls.Dial`, `PingAll` fans out with its own 8-way cap — so for those three, the only existing gate is the general 20rpm/burst-5 token bucket, which bounds one IP's rate but not how many distinct IPs can run checks in parallel. And even for the endpoints that do hold a semaphore slot, the slot is released the instant a fast check finishes — long before the 30-minute retention window is up — so concurrency limits don't translate into a bound on how many *completed* results accumulate. The cache therefore carries its own independent cap, 2000 entries, enforced at insert time.

At capacity, the ephemeral cache evicts the single oldest entry to make room. This is safe specifically because of what's actually lost: the result was already delivered to the client in the original response, so evicting the ephemeral copy only means that one check can't be promoted a little earlier than its natural 30-minute expiry — nothing the client already has disappears.

The **persisted store** (`internal/report.Store`) is what `POST /api/report/promote` writes to and `GET /api/report` serves from. Each promotion gets its own freshly generated ID and its own JSON file under `REPORTS_DIR`, kept for 24 hours from a `captured_at` timestamp stored inside the JSON itself — not file mtime, which wouldn't survive a backup/restore or an operator's `touch`. It has the same 2000 cap as the ephemeral cache, but the opposite eviction policy: at capacity, new promotions are rejected outright, never by evicting an existing report. An existing report may be a link someone is looking at right now; deleting it to make room would break that in a way the ephemeral cache's eviction never can, since nothing external depends on an ephemeral entry surviving.

**Why promote needed its own rate-limit dimension.** Every other limiter in this codebase governs either a subprocess (the semaphores) or general request volume (the token bucket), but promoting a result is neither — it's one disk write, and nothing execs. Left gated only by the general limiter, someone could run repeated cheap checks that never touch a subprocess slot at all (a `BGP` lookup, an `SSLCheck` against a fast host — both complete in single-digit milliseconds) and promote every one, filling the 2000-report cap with junk faster than the general limiter alone would prevent. `Promote` is gated instead by its own `*ratelimit.Limiter` — 10 requests/hour per IP, burst of 3 — built with `ratelimit.NewPerHour`, a second constructor added alongside the existing per-minute `New` specifically because `New`'s `rpm` parameter is an `int` and can't express a sub-1-per-minute rate without truncating to zero and permanently blocking every caller.

**ID entropy.** Both the ephemeral request ID and the promoted report ID use the same scheme (`internal/report.NewID`): 20 bytes of `crypto/rand`, hex-encoded to a fixed 40-character string — 160 bits of entropy, above the ~122 bits of a random UUIDv4. The report ID becomes part of a public URL served by an unauthenticated read endpoint on a system routinely targeted by third-party penetration testers, so it needs to be unguessable and unenumerable, not merely unique. `internal/report.ValidID` checks the fixed length and charset before any client-supplied ID is used to build a filesystem path or look anything up — a client-supplied string is never handed to `filepath.Join` unvalidated.

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
- Client IP for rate limiting and the "Your IP" display is taken from the second-from-last entry of `X-Forwarded-For` if it has at least two comma-separated entries, falling back to `X-Real-IP`, then `RemoteAddr`. This deployment sits behind the WCDN/ParsPack CDN, which was confirmed via packet capture on 2026-08-05 to always append exactly two trusted entries to `X-Forwarded-For` — `[real client IP], [CDN's own hop IP]` — regardless of what a client sends before them, so the second-from-last entry is the CDN's own observation of the real client and cannot be forged by prefixing extra values onto the header.