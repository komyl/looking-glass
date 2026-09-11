# Architecture

## Overview

Two deployed service roles. The **master** serves the UI, holds the BGP table,
and proxies probe requests to agents. The **agent** runs on every measurement
node and executes network operations. Private operator-side `mrt2json` and
`geoipbuilder` tools prepare data offline; they are not deployed services, and
their `cmd/` implementations are not part of the public source checkout.

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

Routes are loaded from a JSON file converted from MRT TABLE_DUMP2 format. Raw
MRT is input to private `mrt2json`, not to the Master. The generated JSON is
read once at startup and again whenever its mtime changes (polled every five
minutes). During reload, the old snapshot continues serving requests until
the new one is fully parsed. A failed reload preserves the old snapshot; a
successful swap is atomic via `sync/atomic.Pointer`. Normal BGP data updates
therefore need no Master restart.

The filesystem roles and raw-to-runtime update procedure are owned by
[INSTALL.md](../INSTALL.md#filesystem-and-data-layout) and
[bgp-data.md](bgp-data.md).

Prefix lookup uses a binary radix trie — one trie for IPv4, one for IPv6. Each node in the trie holds a slice of routes. IP lookup walks the trie bit by bit and returns the deepest matching node (longest prefix match). Prefix lookup walks exactly `prefix_length` bits and returns routes at that node only.

ASN lookup uses a reverse map index built at load time: a `map[int][]Route`
keyed by ASN. Lookup is a direct map access, and the returned result slice is
capped at 1000 routes.

Memory: a full global BGP table (~1.4M prefixes) occupies approximately 2 GB RSS.

---

## GeoIP

The private offline GeoIP pipeline consumes MaxMind Country, MaxMind ASN, and
IPinfo Lite and produces the runtime artifact through this boundary:

```text
source files
→ canonical builder
→ canonical eight-column CSV/CSV.GZ candidate
→ independent source-backed validator
→ fail-closed publication
→ published canonical artifact
```

The schema is exactly:

```text
network,country,country_code,continent,continent_code,asn,as_name,as_domain
```

MaxMind MMDB was not adopted as a Master runtime input format. An earlier runtime-oriented pure Go reader correctly parsed metadata and traversed the trie, but triggered goroutine stack overflow on deeply nested pointer chains in the data section. The CSV runtime loader remained simpler and continued to use the existing trie infrastructure.

The offline builder partitions output at the union of source-prefix boundaries
and applies precedence independently per field. The validator separately
reconstructs those boundaries and expected values, and rejects structural,
ordering, duplicate, overlap, coverage, source-semantic, and deterministic
serialization failures.

Publication rejects a destination that names the same filesystem object as a
source or candidate. It copies the exact validated bytes into a
destination-local staging file, syncs and closes it, atomically renames it
over the published path, and syncs the directory. Validation or pre-rename
failure preserves the previous published artifact. No older generation is
retained after a successful rename.

At runtime, `GEOIP_PATH` supplies exactly one published canonical CSV or
CSV.GZ artifact. A non-empty `GEOIP_PATH2` is invalid configuration and stops
Master startup; the Master neither ignores it nor performs a runtime
multi-source merge. The runtime loader requires the exact schema, exact field
count, valid CIDRs, and complete CSV/GZIP input, including rejection of
physical blank lines outside quoted data. It builds one unpublished snapshot
and installs it only after clean EOF. A detected load failure rejects the
complete GeoIP database, is logged, and leaves the Master running without
GeoIP enrichment.

The installed snapshot uses IPv4 and IPv6 binary radix tries. A hash map
(`map[string]*Record`) is built alongside them in a single pass, keyed by ASN
string (`AS15169`), for O(1) operator name resolution during BGP response
enrichment. GeoIP has no hot reload.

The runtime structural load does not repeat the offline source-semantic or
publication gate. Source precedence, boundary reconstruction, canonical
ordering, duplicate/overlap rejection, deterministic generation, source-backed
field correctness, artifact identities, and publication integrity remain the
offline pipeline's responsibility.

Only the private `geoipbuilder` consumes the provider MMDB/CSV files. The
Master consumes the published canonical artifact once during startup, so a
GeoIP data update requires a Master restart after successful publication. The
filesystem roles and operator procedure are owned by
[INSTALL.md](../INSTALL.md#filesystem-and-data-layout) and
[GeoIp.md](GeoIp.md).

---

## Probe streaming

Ping (single-node mode) and traceroute output is streamed line-by-line via Server-Sent Events. Each line is written as a `data:` field and flushed immediately. The `X-Accel-Buffering: no` response header disables nginx proxy buffering for SSE responses.

WebSocket was evaluated and rejected: it requires a connection upgrade, adds bidirectional framing overhead, and is harder to proxy correctly through nginx without additional configuration.

---

## Multi-node ping

The `/api/ping-all` endpoint fans out to all currently-live agents (see "Agent liveness" below) in parallel using goroutines, bounded to 8 concurrent agent requests at a time. Each agent executes `ping -c 4`, parses the output, and returns structured JSON (`sent`, `received`, `loss`, `rtt_min`, `rtt_avg`, `rtt_max`). The master waits for all goroutines to complete and returns a single JSON response. The UI renders results as a table that populates when the response arrives.

The original ping implementation streamed output from a single selected node via SSE, mirroring traceroute. This was replaced because the parallel table view is more useful for network diagnostics — it shows relative performance across ISPs in a single request.

---

## Agent liveness

A background goroutine, started once from `main.go` alongside the BGP
store's own goroutine, polls every registered agent's `/health` endpoint
every 12 seconds with a 3-second per-check timeout, using the same
`X-Agent-Secret` header as every other master→agent call but a dedicated
`http.Client` — so a slow or dead agent's health check never adds latency
to a real `Proxy`/`PortCheck`/`PingAll` request. Only the HTTP status is
checked; the response body is never parsed or trusted.

A node is marked dead after 2 consecutive failed checks, and live again
after a single successful one — fast recovery is intentional, since a
node flapping back should rejoin node lists and `PingAll` immediately
rather than waiting out a longer confirmation window. At startup, every
node is treated as live while `StartHealthChecker` runs an immediate first
check round; subsequent rounds start on the 12-second ticker.

This state lives in `internal/nodes` as a separate ID-keyed tracker
(`IsLive(id string) bool`, `LiveNodes() []Node`), not as a field on `Node`
itself. Node metadata remains immutable while the checker updates shared
state, and `LiveNodes` filters `nodes.List` in order before returning the
current metadata copies.

Consumers: `Handler.Nodes` (`GET /api/nodes`) returns `nodes.LiveNodes()`
instead of `nodes.List` — dead nodes are simply absent, with no change to
the public response shape. `Handler.PingAll` and `Handler.HTTPCheckAll`
fan out only to `nodes.LiveNodes()` — a dead node gets no entry in
`results` at all, not an `"status": "error"` row. `Handler.Proxy` and
`Handler.PortCheck` check
`nodes.IsLive(node.ID)` immediately after the node lookup succeeds and,
if false, return the same `"agent unreachable"` error already returned on
a real connection failure, without attempting the actual agent request or
waiting out its full timeout (130s for `Proxy`, 10s for `PortCheck`).

---

## Rate limiting

The system keeps these controls as separate boundaries:

**nginx request limiting** — an external, operator-configured boundary. The
generic example in `INSTALL.md` uses 20 requests/second with burst 30 and
returns 429 on breach; those values are example configuration, not
application requirements or a claim about production settings.

**Application token bucket** — per IP, 20 req/min sustained, burst of 5. In-process, no Redis. Implemented in `internal/ratelimit`. Entries are cleaned up after 30 minutes of inactivity.

**Per-IP active-request/subprocess semaphore** — each IP may hold at most one
active subprocess-backed handler request (ping, traceroute, or dig), or one
active request via `/api/proxy` or `/api/portcheck`. Implemented via a
`sync.Map` of buffered `chan struct{}` with capacity 1, cleaned up after 30
minutes of inactivity. It prevents a single IP from holding multiple
long-running requests simultaneously.

A global semaphore (`chan struct{}` with capacity 30) bounds concurrent
top-level ping, traceroute, and dig handler requests across all IPs, and the
same semaphore gates `/api/proxy` and `/api/portcheck`. Dig's internal
resolver fan-out is separately capped at 8 concurrent commands per request.

`/api/bgp` is gated by the application token bucket like every other target-facing endpoint. `/api/myip`, `/api/info`, and `/api/nodes` are deliberately left unthrottled — they're called on every page load, are cheap in-memory lookups, and rate limiting them risks breaking legitimate usage for shared/NAT IPs for negligible security benefit.

---

## Permanent Link reports

Two independent storage layers back this feature, not one, because they answer different questions and need different failure behavior at capacity.

The **ephemeral cache** (`internal/report.EphemeralCache`) holds the actual
result of every completed check — ping, ping-all, traceroute, portcheck, dns,
ssl, bgp, http-check — with a nominal 30-minute retention age in an in-memory
map local to one Master process. Cleanup runs every five minutes. `Get` does
not independently check an entry's age, so an entry can remain promotable
after 30 minutes until a subsequent sweep removes it. The map is not
replicated across Master instances. A `request_id` created on one instance is
unavailable to another unless external routing or session behavior keeps the
promotion request on the same instance. Every check writes into this cache
once its result is fully known, whether or not anyone ever asks to keep it — a
`request_id` JSON field for request/response endpoints, an initial named
`request_id` SSE event (sent before any hop/result data, since a streaming
client needs it up front) for the streaming ones. This happens
unconditionally, independent of `REPORTS_DIR`: the disk layer below can be
entirely unavailable and every check still gets a `request_id`.

Neither the per-IP subprocess semaphore nor the global 30-slot semaphore bounds how large this cache can grow. `BGP`, `SSLCheck`, `PingAll`, and `HTTPCheckAll` never touch either semaphore at all — `BGP` is a pure trie lookup, `SSLCheck` a direct `tls.Dial`, `PingAll` and `HTTPCheckAll` each fan out with their own 8-way cap — so for those four, the only existing gate is the general 20rpm/burst-5 token bucket, which bounds one IP's rate but not how many distinct IPs can run checks in parallel. And even for the endpoints that do hold a semaphore slot, the slot is released the instant a fast check finishes — long before the 30-minute retention window is up — so concurrency limits don't translate into a bound on how many *completed* results accumulate. The cache therefore carries its own independent cap, 2000 entries, enforced at insert time.

At capacity, the ephemeral cache evicts the single oldest entry to make room. This is safe specifically because of what's actually lost: the result was already delivered to the client in the original response, so evicting the ephemeral copy only means that one check can't be promoted before the cleanup sweep would remove it — nothing the client already has disappears.

The **persisted store** (`internal/report.Store`) is what `POST /api/report/promote` writes to and `GET /api/report` serves from. Each promotion gets its own freshly generated ID and its own JSON file under `REPORTS_DIR`, kept for 24 hours from a `captured_at` timestamp stored inside the JSON itself — not file mtime, which wouldn't survive a backup/restore or an operator's `touch`. It has the same 2000 cap as the ephemeral cache, but the opposite eviction policy: at capacity, new promotions are rejected outright, never by evicting an existing report. An existing report may be a link someone is looking at right now; deleting it to make room would break that in a way the ephemeral cache's eviction never can, since nothing external depends on an ephemeral entry surviving.

**Why promote needed its own rate-limit dimension.** Every other limiter in this codebase governs either a subprocess (the semaphores) or general request volume (the token bucket), but promoting a result is neither — it's one disk write, and nothing execs. Left gated only by the general limiter, someone could run repeated cheap checks that never touch a subprocess slot at all (a `BGP` lookup, an `SSLCheck` against a fast host — both complete in single-digit milliseconds) and promote every one, filling the 2000-report cap with junk faster than the general limiter alone would prevent. `Promote` is gated instead by its own `*ratelimit.Limiter` — 10 requests/hour per IP, burst of 3 — built with `ratelimit.NewPerHour`, a second constructor added alongside the existing per-minute `New` specifically because `New`'s `rpm` parameter is an `int` and can't express a sub-1-per-minute rate without truncating to zero and permanently blocking every caller.

**ID entropy.** Both the ephemeral request ID and the promoted report ID use the same scheme (`internal/report.NewID`): 20 bytes of `crypto/rand`, hex-encoded to a fixed 40-character string — 160 bits of entropy, above the ~122 bits of a random UUIDv4. The report ID becomes part of a public URL served by an unauthenticated read endpoint on a system routinely targeted by third-party penetration testers, so it needs to be unguessable and unenumerable, not merely unique. `internal/report.ValidID` checks the fixed length and charset before any client-supplied ID is used to build a filesystem path or look anything up — a client-supplied string is never handed to `filepath.Join` unvalidated.

---

## Input validation

All user-supplied targets pass through `internal/validator` before reaching any subprocess. The validator accepts valid IPv4/IPv6 addresses and RFC-compliant hostnames. It rejects inputs containing shell metacharacters. `exec.Command` is called with arguments as a slice — no shell interpolation occurs at any point.

`internal/validator`'s `ValidateNotPrivate` additionally rejects any target that is, or resolves via DNS to, a loopback, private (RFC 1918/4193), link-local, unspecified, or multicast address — including `169.254.169.254`, the common cloud-metadata endpoint. It does not run on `/api/bgp`'s `ip` lookup, since that only queries the local BGP/GeoIP tries and never opens a connection to the target, nor on `/api/dig`, since `dig` only asks the fixed list of public resolvers (`h.resolvers`) a DNS question about the target name — it never connects to the target address itself, so a private-range target there is not an SSRF vector, and rejecting it would break legitimate reverse-DNS (PTR) lookups against internal IPs. A DNS lookup failure is treated as non-blocking — resolution errors are left for the downstream probe to report.

`ValidateNotPrivate` returns the resolved IP alongside the validation result. For the endpoints that connect to the target directly from the master — `ping`, `traceroute`, `ssl` — that IP is pinned and reused for the actual `ping`/`traceroute` subprocess or `tls.DialWithDialer` call instead of re-resolving the hostname a second time. Without this, a DNS-rebinding attacker could return a public address at validation time and a private one moments later when the real connection is made; pinning closes that window since there's only ever one resolution. `SSLCheck` dials the pinned IP but keeps the original hostname as `tls.Config.ServerName`, so SNI and certificate hostname matching are unaffected. One observable side effect: `ping`/`traceroute`'s own output header now shows the resolved IP rather than the original hostname when a hostname target was given — expected, not a bug.

`proxy`, `portcheck`, `ping-all`, and `http-check` also call `ValidateNotPrivate` (via `ValidateHTTPTarget` for `http-check`), but discard the returned IP and forward the original target string to the agent. This is a master-side check only: it rejects literal private-IP targets and whatever a single DNS resolution says at that instant, but it does not close the rebinding window for these four endpoints, because the actual ping/traceroute/portcheck/HTTP request runs on the agent's host, which independently resolves whatever string it receives. Closing that fully requires the same pinning logic inside private `cmd/agent` tooling, whose implementation is absent from the public GitHub checkout — tracked as separate future work, not implemented here.

`Proxy` and `PortCheck` return a fixed `"agent unreachable"` message on agent-connection failure, with none of the underlying error text included — there is nothing in that response to sanitize or leak, regardless of what shape the underlying network error takes.

`SSLCheck` separately uses a `sanitizeErr` helper that strips `dial tcp <src>->` and similar prefixes from error strings. This only rewrites errors that contain that `->` pattern (established-connection read/write failures); it does not rewrite `dial tcp <addr>: connect: connection refused` (connection-never-established failures), which is the common case when a target simply isn't listening. That gap is a known limitation, not yet fixed — it's lower severity than the `Proxy`/`PortCheck` case because the address exposed there is the user's own requested target, not an internal secret.

---

## Security model

- Agent endpoints require `X-Agent-Secret` header. The secret is a 32-byte random hex string shared across all nodes.
- Agent port 9090 is restricted to the master IP via ufw on each agent node.
- The agent URL, IP, and secret are never returned to clients. The
  `/api/nodes` endpoint returns only public metadata (ID, name, location).
- The master binary is deployed behind nginx. It binds `127.0.0.1:8082` and is not directly reachable from the internet.
- Client IP for rate limiting and the "Your IP" display is taken from the second-from-last entry of `X-Forwarded-For` if it has at least two comma-separated entries, falling back to `X-Real-IP`, then `RemoteAddr`. This deployment sits behind a CDN confirmed via packet capture to always append exactly two trusted entries to `X-Forwarded-For` — `[real client IP], [CDN's own hop IP]` — regardless of what a client sends before them, so the second-from-last entry is the CDN's own observation of the real client and cannot be forged by prefixing extra values onto the header.
