# Looking Glass

A high-performance, self-hosted network looking glass written in Go. Designed for ISPs, data centers, and network operators who require full control over their infrastructure with zero external dependencies at runtime.

Supports multi-node distributed probing, BGP route lookup from local MRT dumps, DNS resolution, SSL certificate inspection, and real-time streaming of ping and traceroute via Server-Sent Events.

---

## Table of Contents

- [Architecture](#architecture)
- [Requirements](#requirements)
- [Building](#building)
- [Master Node Setup](#master-node-setup)
- [Agent Node Setup](#agent-node-setup)
- [BGP Data](#bgp-data)
- [Nginx Configuration](#nginx-configuration)
- [systemd Services](#systemd-services)
- [Adding a New Node](#adding-a-new-node)
- [Security](#security)
- [File Layout](#file-layout)

---

## Architecture

The system consists of two binaries:

**`looking-glass`** — the master process. Serves the web UI, handles BGP lookups from an in-memory radix trie, and proxies probe requests to agent nodes over HTTP with a shared secret. Runs on the primary node only.

**`agent`** — a lightweight probe daemon. Executes `ping` and `traceroute` and streams results back to the master via SSE. Runs on every node including the primary.

```
                        ┌─────────────────────────────┐
                        │        Master Node           │
         User ──HTTPS──▶│   nginx → looking-glass      │
                        │       port 8082               │
                        │                              │
                        │   agent (127.0.0.1:9090)     │
                        └──────────┬───────────────────┘
                                   │ HTTP + X-Agent-Secret
                    ┌──────────────┼──────────────┐
                    ▼              ▼              ▼
              Node 1               Node 2       Node 3
             agent:9090          agent:9090    agent:9090
```

BGP data is loaded from a local JSON file (converted from MRT dump). The store uses an atomic pointer swap on reload — zero downtime, no locks on the hot path.

Rate limiting is token bucket per IP at the application layer, with a separate nginx `limit_req` zone for the `/api/` path. Each IP is additionally constrained to one concurrent subprocess via a per-IP semaphore backed by `sync.Map`.

---

## Requirements

### Master Node
- Ubuntu 24.04 / Debian 13
- Go 1.22+
- nginx
- `traceroute`, `dnsutils` (`dig`)
- `bgpdump` (for MRT processing)
- fail2ban
- ufw
- Minimum: 4 cores, 8 GB RAM (BGP JSON loads ~2 GB into memory)

### Agent Nodes
- Ubuntu 24.04 / Debian 13
- `ping`, `traceroute` (from `iputils-ping`, `traceroute`)
- Minimum: 2 cores, 4 GB RAM

### Build Host
Any machine with Go 1.22+ installed. Cross-compilation is supported.

---

## Building

Clone or extract the source into `/opt/somename/looking-glass`.

```sh
cd /opt/somename/looking-glass

# Build master binary
go build -ldflags="-s -w" -trimpath -o looking-glass .

# Build agent binary (Linux amd64)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o agent ./cmd/agent/

# Build MRT converter
go build -ldflags="-s -w" -trimpath -o mrt2json ./cmd/mrt2json/
```

The master binary embeds `web/index.html` at compile time via `//go:embed`. Any change to the HTML requires a rebuild.

---

## Master Node Setup

### 1. Install system packages

```sh
apt update
apt install -y nginx traceroute dnsutils bgpdump fail2ban ufw
```

### 2. Create directory layout

```sh
mkdir -p /var/lib/looking-glass
cp looking-glass /usr/local/bin/looking-glass
```

### 3. Prepare BGP data

Download an MRT RIB dump from RIPE RIS:

```sh
# rrc00 = Amsterdam, full table, ~80 MB compressed
wget https://data.ris.ripe.net/rrc00/latest-bview.gz
```

Convert to the internal JSON format:

```sh
./mrt2json latest-bview.gz /var/lib/looking-glass/bgp.json
```

This produces a flat JSON file of ~260 MB. Conversion takes 10–15 minutes depending on CPU. The master hot-reloads automatically when the file modification time changes (checked every 5 minutes). To force an immediate reload, restart the service.

Update BGP data on a schedule by replacing the file and restarting:

```sh
# Run from cron or a deploy script on an external machine
wget -q https://data.ris.ripe.net/rrc00/latest-bview.gz -O latest-bview.gz
./mrt2json latest-bview.gz /var/lib/looking-glass/bgp.json
systemctl restart looking-glass
```

### 4. Install the master service

```sh
cat > /etc/systemd/system/looking-glass.service << 'EOF'
[Unit]
Description=Looking Glass
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/komyl/looking-glass
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

### 5. Verify

```sh
curl -s http://127.0.0.1:8082/api/info
# {"bgp_updated":"2026-04-29 22:36 UTC","route_count":1374785}
```

---

## Agent Node Setup

The agent runs on every probe node, including the master. On the master it binds to `127.0.0.1:9090`. On remote nodes it binds to `0.0.0.0:9090` and must be firewalled to accept connections only from the master's IP.

### 1. Copy the binary

```sh
# From the master node
scp agent root@<NODE_IP>:/usr/local/bin/looking-glass-agent
chmod +x /usr/local/bin/looking-glass-agent
```

### 2. Install system packages on the agent node

```sh
apt update
apt install -y iputils-ping traceroute
```

### 3. Install the agent service

**On the master node** (`LISTEN_ADDR=127.0.0.1:9090`):

```sh
cat > /etc/systemd/system/looking-glass-agent.service << 'EOF'
[Unit]
Description=Looking Glass Agent
After=network.target

[Service]
Type=simple
Environment=AGENT_SECRET=<YOUR_SECRET>
Environment=LISTEN_ADDR=127.0.0.1:9090
ExecStart=/usr/local/bin/looking-glass-agent
Restart=on-failure
RestartSec=5
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
```

**On remote agent nodes** (`LISTEN_ADDR=0.0.0.0:9090`):

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
```

```sh
systemctl daemon-reload
systemctl enable --now looking-glass-agent
```

### 4. Generate the shared secret

Generate once and use the same value across all nodes:

```sh
openssl rand -hex 32
```

Set this value as `AGENT_SECRET` in every service file.

### 5. Verify agent connectivity

```sh
curl -s -H "X-Agent-Secret: <YOUR_SECRET>" http://<NODE_IP>:9090/health
# ok
```

---

## BGP Data

### MRT Format

The converter (`mrt2json`) reads TABLE_DUMP2 MRT files produced by RIPE RIS and RouteViews. It deduplicates prefixes (first-seen wins), skips malformed entries, and writes a single JSON file.

Output format:

```json
{
  "timestamp": 1745967419,
  "routes": [
    {
      "prefix": "1.0.0.0/24",
      "nexthop": "80.77.16.114",
      "aspath": [13335, 15169],
      "origin": "igp",
      "localpref": 100,
      "med": 0,
      "communities": ["13335:10000"]
    }
  ]
}
```

### Lookup behavior

**IP lookup** — longest prefix match using a binary radix trie. O(32) for IPv4, O(128) for IPv6.

**Prefix lookup** — exact match only.

**ASN lookup** — returns up to 1000 routes whose AS-path contains the queried ASN. Results are capped in the response to prevent excessive rendering.

### Updating BGP data

The service watches the file's modification time. Drop a new `bgp.json` into `/var/lib/looking-glass/` and the service reloads within 5 minutes. For immediate effect:

```sh
systemctl restart looking-glass
```

During reload the old snapshot remains in memory and continues serving requests until the new one is fully parsed and atomically swapped in.

---

## Nginx Configuration

Create `/etc/nginx/sites-available/lookinglass`:

```nginx
server {
    listen 80;
    server_name domain.ir www.domain.ir;

    include /etc/nginx/snippets/security_headers.conf;

    limit_req zone=ddos_limit burst=20 nodelay;

    location / {
        proxy_pass         http://127.0.0.1:8082;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering           off;
        proxy_cache               off;
        proxy_read_timeout        130s;
        proxy_set_header          X-Accel-Buffering no;
    }

    location /api/ {
        limit_req zone=api_limit burst=3 nodelay;
        limit_req_status 429;
        proxy_pass         http://127.0.0.1:8082;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering           off;
        proxy_cache               off;
        proxy_read_timeout        130s;
        proxy_set_header          X-Accel-Buffering no;
    }
}
```

Add rate limit zones to `/etc/nginx/nginx.conf` inside the `http {}` block:

```nginx
limit_req_zone $binary_remote_addr zone=ddos_limit:10m rate=20r/s;
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=6r/m;
```

Enable and reload:

```sh
ln -s /etc/nginx/sites-available/bgpx /etc/nginx/sites-enabled/bgpx
nginx -t && systemctl reload nginx
```

**Critical:** The `/api/` location uses `proxy_buffering off`. Without this, SSE streams (ping, traceroute) will not flush to the client in real time. The `X-Accel-Buffering: no` header disables buffering at the nginx level for upstream-set responses as well.

---

## systemd Services

| Service | Node | Binary | Port |
|---|---|---|---|
| `looking-glass` | Master only | `looking-glass` | `127.0.0.1:8082` |
| `looking-glass-agent` | All nodes | `agent` | `127.0.0.1:9090` (master), `0.0.0.0:9090` (remotes) |

Both services are configured with `Restart=on-failure` and `WantedBy=multi-user.target`, so they come up automatically after a reboot.

---

## Adding a New Node

### 1. Provision the node

Install packages:

```sh
apt update && apt install -y iputils-ping traceroute
```

Copy and install the agent:

```sh
scp agent root@<NEW_NODE_IP>:/usr/local/bin/looking-glass-agent
chmod +x /usr/local/bin/looking-glass-agent
```

Create the service with `LISTEN_ADDR=0.0.0.0:9090` and the shared `AGENT_SECRET`. Enable and start it.

### 2. Firewall the agent port

On the new node, allow port 9090 only from the master:

```sh
ufw allow from mian_node(0.0.0.0) to any port 9090
ufw reload
```

### 3. Verify connectivity from the master

```sh
curl -s -H "X-Agent-Secret: <YOUR_SECRET>" http://<NEW_NODE_IP>:9090/health
# ok
```

### 4. Register the node in source

Edit `internal/nodes/nodes.go` and append a new entry to the `List` slice:

```go
{
    ID:       "newnode",
    Name:     "City — ISP Name",
    Location: "City",
    ISP:      "ISP Name",
    IP:       "<NEW_NODE_IP>",
    URL:      "http://<NEW_NODE_IP>:9090",
},
```

`ID` must be lowercase alphanumeric, unique, and URL-safe. `IP` is displayed publicly; `URL` is internal and never exposed to clients.

### 5. Rebuild and deploy

```sh
cd /opt/someuser/looking-glass
go build -ldflags="-s -w" -trimpath -o looking-glass .
systemctl restart looking-glass
```

The new node appears in the source selector immediately after restart.

---

## Security

### Secret rotation

To rotate the agent secret, update `AGENT_SECRET` in every node's service file and in `internal/nodes/nodes.go`, then rebuild the master and restart all services. There is no grace period — old and new secrets cannot coexist.

### Network isolation

Agent port 9090 must not be exposed to the public internet. The ufw rule on each agent node restricts access to the master IP only. Verify with:

```sh
ufw status verbose
```

### Rate limiting layers

| Layer | Mechanism | Limit |
|---|---|---|
| nginx general | `limit_req zone=ddos_limit` | 20 req/s per IP |
| nginx API | `limit_req zone=api_limit` | 6 req/min per IP |
| Application | Token bucket | 20 req/min per IP |
| Application | Global semaphore | 30 concurrent subprocesses |
| Application | Per-IP semaphore | 1 concurrent subprocess per IP |

### fail2ban

Two jails are active: `sshd` and `looking-glass`. The looking glass jail watches for HTTP 429 responses in the nginx access log and bans IPs that exceed the threshold.

Check jail status:

```sh
fail2ban-client status looking-glass
fail2ban-client status sshd
```

### Input validation

All user-supplied targets pass through `validator.ValidateTarget` before reaching any subprocess. The validator rejects inputs containing shell metacharacters and accepts only valid IP addresses or RFC-compliant hostnames. `exec.Command` is used directly — no shell interpolation occurs.

---

## File Layout

```
/opt/someuser/looking-glass/
├── main.go                        # Master entry point, HTTP routing
├── go.mod
├── looking-glass                  # Compiled master binary
├── agent                          # Compiled agent binary
├── mrt2json                       # MRT to JSON converter
├── latest-bview.gz                # Raw MRT dump (RIPE RIS rrc00)
├── web/
│   └── index.html                 # UI — embedded into binary at build time
├── cmd/
│   ├── agent/main.go              # Agent source
│   └── mrt2json/main.go          # MRT converter source
└── internal/
    ├── bgp/
    │   ├── store.go               # BGP store: JSON load, hot-reload, atomic swap
    │   └── trie.go                # Binary radix trie for prefix lookup
    ├── handler/
    │   ├── handler.go             # HTTP handlers: ping, traceroute, dig, ssl, bgp
    │   └── proxy.go               # Node list endpoint and agent proxy handler
    ├── nodes/
    │   └── nodes.go               # Node registry and shared secret
    ├── ratelimit/
    │   └── limit.go               # Token bucket rate limiter
    └── validator/
        └── valid.go               # Input sanitization

/var/lib/looking-glass/
└── bgp.json                       # Processed BGP routing table (~260 MB)

/etc/systemd/system/
├── looking-glass.service
└── looking-glass-agent.service

/usr/local/bin/
├── looking-glass
└── looking-glass-agent
```

---

## Environment Variables

### looking-glass

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `127.0.0.1:8082` | TCP address to bind |
| `BGP_DATA_PATH` | `/var/lib/looking-glass/bgp.json` | Path to processed BGP JSON |

### looking-glass-agent

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `0.0.0.0:9090` | TCP address to bind |
| `AGENT_SECRET` | *(required)* | Shared secret for request authentication |