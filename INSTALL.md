# Installation

## Building

Requires Go 1.22 or later. No external Go dependencies.

```sh
# Master binary
go build -ldflags="-s -w" -trimpath -o looking-glass .

# Agent binary (cross-compile for Linux amd64)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o agent ./cmd/agent/

# MRT converter (run on update, not deployed to servers)
go build -ldflags="-s -w" -trimpath -o mrt2json ./cmd/mrt2json/
```

The HTML UI is embedded into the master binary at compile time via `//go:embed`. Any change to `web/index.html` requires a rebuild and service restart.

---

## Master node

Requirements: Debian 13 or Ubuntu 24.04, 4+ cores, 8+ GB RAM.

```sh
apt update
apt install -y nginx traceroute dnsutils bgpdump fail2ban ufw

mkdir -p /var/lib/looking-glass
cp looking-glass /usr/local/bin/looking-glass
```

### BGP data

Download a full RIB snapshot from RIPE RIS and convert it:

```sh
wget https://data.ris.ripe.net/rrc00/latest-bview.gz
./mrt2json latest-bview.gz /var/lib/looking-glass/bgp.json
```

Conversion takes 10–15 minutes and produces a ~260 MB JSON file. The service polls the file's mtime every 5 minutes and reloads automatically when it changes.

### GeoIP data

Obtain an ipinfo Lite CSV (plain or gzip). Place it at the path configured by `GEOIP_PATH`. See [doc/geoip.md](doc/geoip.md).

### systemd service

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
Environment=GEOIP_PATH=/opt/ipinfo/ipinfo_lite.csv.gz
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

### nginx

```nginx
server {
    listen 80;
    server_name your.domain;

    include /etc/nginx/snippets/security_headers.conf;

    limit_req zone=ddos_limit burst=30 nodelay;
    limit_req_status 429;

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
}
```

Add to `nginx.conf` inside `http {}`:

```nginx
limit_req_zone $binary_remote_addr zone=ddos_limit:10m rate=20r/s;
```

**Important:** do not include `error_pages.conf` snippets in this server block. Error page redirects return HTML to the client, which breaks JSON API responses and causes parse errors in the UI.

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

## Agent nodes

Requirements: Debian 13 or Ubuntu 24.04, 2+ cores, 4+ GB RAM.

```sh
apt update && apt install -y iputils-ping traceroute
```

Generate a shared secret once and use it across all nodes:

```sh
openssl rand -hex 32
```

Copy the agent binary and install the service. On remote nodes, bind on all interfaces. On the master, bind loopback only.

```sh
# Remote node
ssh root@<NODE_IP> systemctl stop looking-glass-agent
scp agent root@<NODE_IP>:/usr/local/bin/looking-glass-agent
ssh root@<NODE_IP> "cat > /etc/systemd/system/looking-glass-agent.service << 'EOF'
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
systemctl daemon-reload && systemctl enable --now looking-glass-agent"
```

Restrict port 9090 to the master IP on each agent node:

```sh
ufw allow from <MASTER_IP> to any port 9090
ufw reload
```

Verify from the master:

```sh
curl -s -H "X-Agent-Secret: <YOUR_SECRET>" http://<NODE_IP>:9090/health
# ok
```

---

## Environment variables

| Binary | Variable | Default | Description |
|---|---|---|---|
| master | `LISTEN_ADDR` | `127.0.0.1:8082` | TCP bind address |
| master | `BGP_DATA_PATH` | `/var/lib/looking-glass/bgp.json` | BGP data file |
| master | `GEOIP_PATH` | `/opt/ipinfo/ipinfo_lite.csv.gz` | ipinfo Lite CSV |
| agent  | `LISTEN_ADDR` | `0.0.0.0:9090` | TCP bind address |
| agent  | `AGENT_SECRET` | *(required)* | Shared secret for authentication |