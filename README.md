# looking-glass

A self-hosted network looking glass written in Go. Built for ISPs, data centers, and network operators who need full runtime autonomy — no external API calls, no CDN dependencies, no telemetry.

The system is split into two binaries: a **master** that serves the UI, holds the BGP table in memory, and orchestrates probes; and an **agent** that runs on each measurement node and executes the actual network operations. Communication between master and agents uses plain HTTP with a pre-shared secret over private networking.

---

## Features

### Network probing
- **Ping** — runs against all nodes simultaneously. Results rendered in a table: sent/received, loss%, RTT min/avg/max per node. No node selection required.
- **Traceroute** — user selects source node. Output streamed in real time via SSE.
- **Port check** — single TCP connect to target:port with 5s timeout. Returns `open`, `closed`, or `filtered`. Supports direct IP or hostname. `http://` and `https://` prefixes are stripped before resolution.
- **DNS lookup** — executes `dig` on the master against a configured resolver. Supports A, AAAA, MX, NS, TXT, CNAME, SOA, PTR.
- **SSL certificate inspection** — direct TLS dial to target:443 (or custom port). Returns subject, issuer, validity window, days remaining, SANs. On validation failure, falls back to `InsecureSkipVerify` and reports the cert alongside the error.

### BGP routing table
- Loaded from a local JSON file converted from MRT TABLE_DUMP2 format (RIPE RIS, RouteViews, or any compliant source).
- IPv4 and IPv6 prefix lookup via binary radix trie — O(32) and O(128) worst case respectively.
- Lookup modes: longest-prefix match by IP, exact prefix, ASN (returns up to 1000 routes whose AS-path contains the queried ASN).
- Hot-reload: the store polls the file's mtime every 5 minutes and swaps the snapshot atomically. Old data continues serving during reload. No restart required.
- Memory footprint: a full global BGP table (~1.4M prefixes) loads into approximately 2 GB RSS.

### Client IP detection
Reads `X-Forwarded-For` first, then `X-Real-IP`, then `RemoteAddr`. Works correctly behind nginx or any reverse proxy that sets standard forwarding headers.

### Rate limiting
Three independent layers:
1. nginx `limit_req` — 20 req/s per IP on general traffic, 6 req/min on `/api/`.
2. Application-level token bucket — 20 req/min per IP, burst 5, in-process, no Redis.
3. Per-IP subprocess semaphore — each IP may hold at most one active ping/traceroute/dig process at a time, backed by `sync.Map` of buffered channels.

Global subprocess semaphore caps total concurrent `exec.Command` invocations at 30.

---

## Design decisions

**Next-hop is not shown for BGP results.** The MRT dump is collected from a single RIPE RIS collector peer. Every route's next-hop reflects that peer's perspective, not the local routing table. Displaying it would be misleading. The kernel FIB (`ip route get`) was evaluated as an alternative but returns only the default gateway on a typical VPS. AS Path, origin, and communities are shown instead.

**No GeoIP.** A MaxMind GeoLite2 reader was implemented in pure Go (no cgo, no external libraries). The reader correctly parsed metadata and traversed the radix trie but triggered goroutine stack overflow on deeply nested pointer chains in the MMDB data section. The fix (depth cap at 64) was implemented but the feature was deferred to avoid destabilizing production during initial rollout. The reader is present in the repository and can be re-enabled.

**SSE over WebSocket for streaming.** Traceroute and single-node ping stream output line-by-line via Server-Sent Events. SSE was chosen because it is unidirectional, trivially proxied through nginx with `proxy_buffering off`, and requires no connection upgrade. The `X-Accel-Buffering: no` response header disables nginx's internal buffer for proxied SSE responses.

**Ping changed from single-node SSE to parallel JSON.** The original ping implementation mirrored traceroute — one node, output streamed. This was replaced with a fan-out model: the master fires concurrent goroutines to all agents, each agent parses its own `ping` output and returns structured JSON, and the master collects all results in a single HTTP response. The UI renders a table with RTT and loss per node.

---

## Repository layout

```
.
├── main.go                    # Master entry point, mux registration
├── go.mod
├── web/
│   └── index.html             # Full UI, embedded into binary at build time via //go:embed
├── cmd/
│   ├── agent/
│   │   └── main.go            # Agent binary: ping, traceroute, portcheck, ping-summary
│   └── mrt2json/
│       └── main.go            # Offline MRT-to-JSON converter
└── internal/
    ├── bgp/
    │   ├── store.go           # JSON loader, mtime watcher, atomic snapshot swap
    │   └── trie.go            # IPv4/IPv6 binary radix trie
    ├── handler/
    │   ├── handler.go         # HTTP handlers: myip, info, ping, traceroute, dig, ssl, bgp
    │   └── proxy.go           # /api/nodes, /api/proxy, /api/portcheck, /api/ping-all
    ├── nodes/
    │   └── nodes.go           # Node registry (ID, name, ISP, internal URL, shared secret)
    ├── ratelimit/
    │   └── limit.go           # Token bucket, per-key, cleanup goroutine
    └── validator/
        └── valid.go           # Target sanitization (IP, hostname, CIDR, ASN)
```

Runtime data:

```
/var/lib/looking-glass/
└── bgp.json                   # Processed routing table (~260 MB for full global table)

/usr/local/bin/
├── looking-glass              # Master binary (~6 MB stripped)
└── looking-glass-agent        # Agent binary (~5 MB stripped)
```

---

## Building

Requires Go 1.22 or later. No external Go dependencies — stdlib only.

```sh
# Master
go build -ldflags="-s -w" -trimpath -o looking-glass .

# Agent (cross-compile for Linux amd64 from any platform)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o agent ./cmd/agent/

# MRT converter (run once per data update, not deployed)
go build -ldflags="-s -w" -trimpath -o mrt2json ./cmd/mrt2json/
```

The HTML UI is embedded at compile time. Any edit to `web/index.html` requires a rebuild and service restart.

---

## BGP data

The converter reads TABLE_DUMP2 MRT files. It deduplicates prefixes (first-seen wins), skips malformed records, and writes a flat JSON array.

Download a full RIB snapshot:

```sh
wget https://data.ris.ripe.net/rrc00/latest-bview.gz
```

Convert:

```sh
./mrt2json latest-bview.gz /var/lib/looking-glass/bgp.json
```

Processing a full table (~1.4M unique prefixes) takes 10–15 minutes. The resulting file is ~260 MB. The service auto-reloads when the file changes; to force immediate reload restart the service.

JSON schema:

```json
{
  "timestamp": 1746000000,
  "routes": [
    {
      "prefix":      "1.0.0.0/24",
      "nexthop":     "80.77.16.114",
      "aspath":      [13335, 15169],
      "origin":      "igp",
      "localpref":   100,
      "med":         0,
      "communities": ["13335:10000"]
    }
  ]
}
```

---

## Deployment

### Master node

Requirements: Debian 13 or Ubuntu 24.04, 4+ cores, 8+ GB RAM, nginx.

```sh
apt update
apt install -y nginx traceroute dnsutils bgpdump fail2ban ufw

mkdir -p /var/lib/looking-glass
cp looking-glass /usr/local/bin/looking-glass
```

Service unit:

```sh
cat > /etc/systemd/system/looking-glass.service << 'EOF'
[Unit]
Description=Looking Glass
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/looking-glass
Environment=BGP_DATA_PATH=/var/lib/looking-glass/bgp.json
Environment=LISTEN_ADDR=127.0.0.1:8082
ExecStart=/usr/local/bin/looking-glass
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now looking-glass
```

Verify:

```sh
curl -s http://127.0.0.1:8082/api/info
```

### Agent nodes

Requirements: Debian 13 or Ubuntu 24.04, 2+ cores, 4+ GB RAM.

```sh
apt update && apt install -y iputils-ping traceroute
```

Copy binary from master build host:

```sh
ssh root@<NODE_IP> systemctl stop looking-glass-agent
scp agent root@<NODE_IP>:/usr/local/bin/looking-glass-agent
ssh root@<NODE_IP> systemctl start looking-glass-agent
```

Generate shared secret once, reuse across all nodes:

```sh
openssl rand -hex 32
```

Service unit (remote nodes bind `0.0.0.0:9090`; master binds `127.0.0.1:9090`):

```sh
cat > /etc/systemd/system/looking-glass-agent.service << 'EOF'
[Unit]
Description=Looking Glass Agent
After=network.target

[Service]
Type=simple
Environment=AGENT_SECRET=<YOUR_SECRET>
Environment=LISTEN_ADDR=0.0.0.0:9090
ExecStart=/usr/local/bin/looking-glass-agent
Restart=on-failure
RestartSec=5
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now looking-glass-agent
```

Verify from master:

```sh
curl -s -H "X-Agent-Secret: <YOUR_SECRET>" http://<NODE_IP>:9090/health
```

### Nginx

```nginx
server {
    listen 80;
    server_name your.domain;

    limit_req zone=ddos_limit burst=20 nodelay;

    location / {
        proxy_pass         http://127.0.0.1:8082;
        proxy_http_version 1.1;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_buffering    off;
        proxy_cache        off;
        proxy_read_timeout 130s;
        proxy_set_header   X-Accel-Buffering no;
    }

    location /api/ {
        limit_req        zone=api_limit burst=3 nodelay;
        limit_req_status 429;
        proxy_pass         http://127.0.0.1:8082;
        proxy_http_version 1.1;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_buffering    off;
        proxy_cache        off;
        proxy_read_timeout 130s;
        proxy_set_header   X-Accel-Buffering no;
    }
}
```

Add to `nginx.conf` inside `http {}`:

```nginx
limit_req_zone $binary_remote_addr zone=ddos_limit:10m rate=20r/s;
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=6r/m;
```

### fail2ban

```sh
cat > /etc/fail2ban/jail.d/looking-glass.conf << 'EOF'
[looking-glass]
enabled  = true
port     = http,https
filter   = looking-glass
logpath  = /var/log/nginx/access.log
maxretry = 10
findtime = 60
bantime  = 600
EOF

cat > /etc/fail2ban/filter.d/looking-glass.conf << 'EOF'
[Definition]
failregex = ^<HOST> .* "(GET|POST) /api/.*" 429
ignoreregex =
EOF

fail2ban-client reload
```

---

## Adding a node

1. Provision host, install `iputils-ping traceroute`.
2. Copy agent binary, install and start service with shared secret and `LISTEN_ADDR=0.0.0.0:9090`.
3. On the new node, restrict port 9090 to master IP only:
   ```sh
   ufw allow from <MASTER_IP> to any port 9090
   ufw reload
   ```
4. Edit `internal/nodes/nodes.go`, append to `List`:
   ```go
   {
       ID:       "nodeid",       // lowercase alphanumeric, URL-safe, unique
       Name:     "City — ISP",
       Location: "City",
       ISP:      "ISP Name",
       IP:       "<NODE_IP>",    // returned in /api/nodes, never the internal URL
       URL:      "http://<NODE_IP>:9090",  // internal only, never exposed to clients
   },
   ```
5. Rebuild master and restart:
   ```sh
   go build -ldflags="-s -w" -trimpath -o looking-glass .
   systemctl restart looking-glass
   ```

---

## API reference

Master endpoints:

| Method | Path | Params | Response |
|---|---|---|---|
| GET | `/` | — | HTML UI |
| GET | `/api/myip` | — | `{"ip":"..."}` |
| GET | `/api/info` | — | `{"route_count":N,"bgp_updated":"..."}` |
| GET | `/api/ping` | `target`, `count` (1–20) | SSE stream, single node |
| GET | `/api/traceroute` | `target`, `maxhops` (5–64) | SSE stream, single node |
| GET | `/api/dig` | `target`, `qtype` | SSE stream |
| GET | `/api/ssl` | `target` (host or host:port) | JSON cert info |
| GET | `/api/bgp` | `type` (ip\|prefix\|asn), `query` | JSON routes |
| GET | `/api/nodes` | — | JSON array of public node metadata |
| GET | `/api/proxy` | `node`, `action` (ping\|traceroute), forwarded params | SSE proxy |
| GET | `/api/portcheck` | `node`, `target`, `port` | JSON port result |
| GET | `/api/ping-all` | `target` | JSON, all nodes in parallel |

Agent endpoints (require `X-Agent-Secret` header):

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Returns `ok` |
| GET | `/ping` | SSE stream |
| GET | `/traceroute` | SSE stream |
| GET | `/portcheck` | `{"target","port","status","latency_ms","error"}` |
| GET | `/ping-summary` | `{"sent","received","loss","rtt_min","rtt_avg","rtt_max","error"}` |

Port check status values: `open`, `closed`, `filtered`.
Ping-all status values per node: `ok`, `degraded`, `down`, `error`.

---

## Environment variables

| Binary | Variable | Default | Description |
|---|---|---|---|
| master | `LISTEN_ADDR` | `127.0.0.1:8082` | Bind address |
| master | `BGP_DATA_PATH` | `/var/lib/looking-glass/bgp.json` | BGP data file path |
| agent | `LISTEN_ADDR` | `0.0.0.0:9090` | Bind address |
| agent | `AGENT_SECRET` | *(required)* | Pre-shared secret for request authentication |