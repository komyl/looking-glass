# Installation

## Building

Requires Go 1.22 or later. No external Go dependencies.

```sh
# Master binary
go build -ldflags="-s -w" -trimpath -o looking-glass .
```

The public GitHub checkout contains the Master source built above. The
`agent`, `mrt2json`, and `geoipbuilder` implementations are private operator
tooling and cannot be built from `cmd/` paths in the public checkout. Operators
must supply authorized builds of those tools; the sections below document
their deployment or operational interfaces.

The HTML UI is embedded into the master binary at compile time via `//go:embed`. Any change to files inside the `web/` directory (including `index.html`, `css/`, and `js/`) requires a rebuild of the binary and service restart.

---

## Filesystem and data layout

An installation should give each file role one predictable home without
assuming a project-mandated application or provider directory. Source-defined
defaults are identified below; all placeholder paths are selected by the
operator and must be used consistently.

| Location | Path category | Classification | Producer | Consumer | Data-update action |
|---|---|---|---|---|---|
| `<application-dir>/` | Operator-selected | Application source and binaries | Build/operator workflow | Master service and operator | Rebuild/restart only when deployed application or embedded frontend changes |
| `<bgp-source-dir>/latest-bview.gz` | Operator-selected | Persistent raw/provider BGP input | Operator transfer from the selected MRT provider | Private `mrt2json` only | No Master action; changing this file alone does not update runtime data |
| `BGP_DATA_PATH` | Built-in default: `/var/lib/looking-glass/bgp.json` | Authoritative generated BGP runtime artifact | Private `mrt2json` | Master BGP store | Hot reload after a successful replacement; no restart solely for a normal data refresh |
| `<geoip-source-dir>/GeoLite2-Country.mmdb` | Operator-selected | Persistent GeoIP provider input | Provider/operator update | Private `geoipbuilder` only | No immediate Master action; build, validate, publish, then restart |
| `<geoip-source-dir>/GeoLite2-ASN.mmdb` | Operator-selected | Persistent GeoIP provider input | Provider/operator update | Private `geoipbuilder` only | No immediate Master action; build, validate, publish, then restart |
| `<geoip-source-dir>/ipinfo_lite.csv.gz` | Operator-selected | Persistent GeoIP provider input | Provider/operator update | Private `geoipbuilder` only | No immediate Master action; build, validate, publish, then restart |
| `<temporary-candidate.csv.gz>` | Operator-selected temporary path | Non-authoritative GeoIP candidate | Private `geoipbuilder` | Private `geoipbuilder` validator/publisher | Remove after successful publication and verification, or after abandoning the update |
| `GEOIP_PATH` | Operator-selected published path | Authoritative published GeoIP runtime artifact | Private `geoipbuilder` after independent validation | Master startup | Master restart required; GeoIP has no hot reload |
| `REPORTS_DIR` | Built-in default: `/var/lib/looking-glass/reports` | Persistent application output | Master report store | Master report store | No service action for ordinary report writes |

The Master does not consume the raw MRT or GeoIP provider files. It consumes
the generated JSON selected by `BGP_DATA_PATH` and the published canonical
CSV or CSV.GZ selected by `GEOIP_PATH`. Set `GEOIP_PATH` explicitly to the
operator-selected published artifact. The source retains
`/var/lib/looking-glass/ipinfo_lite.csv.gz` as a legacy fallback when
`GEOIP_PATH` is unset; despite that filename, the file must contain the current
canonical eight-column schema and is not an IPinfo provider input.

`GEOIP_PATH2` must remain unset. It is unsupported, and a non-empty value is
invalid configuration that stops Master startup.

Raw/provider inputs and authoritative runtime artifacts are persistent. A
GeoIP candidate must use a temporary operator-controlled path that satisfies
the builder's directory and path-safety requirements. The candidate and the
destination-local publication staging file are non-authoritative working
state. Remove the candidate after successful publication and verification.
Do not accumulate persistent generations with names such as `.old`,
`.backup`, `.final2`, or `.new-final`. The BGP converter's
destination-adjacent `.tmp` file is likewise staging, not a second runtime
artifact.

An authorized local operator checkout can contain the following ignored
private source tree:

```text
cmd/
├── agent/
│   └── main.go
├── mrt2json/
│   └── main.go
└── geoipbuilder/
    ├── main.go
    ├── mmdb.go
    └── validator.go
```

`agent/main.go` implements the measurement-node service deployed as
`looking-glass-agent`; it runs on measurement nodes and does not participate
in Master data updates.
`mrt2json/main.go` implements the offline MRT-to-BGP-JSON conversion command.
For `geoipbuilder`, `main.go` owns the CLI and candidate generation,
`mmdb.go` owns MaxMind input traversal, and `validator.go` owns independent
validation and fail-closed publication. This tree describes an authorized
operator checkout only. The paths remain ignored and untracked, and none of
these implementations or placeholder files is distributed in the public
GitHub checkout.

---

## Master node

Requirements: Debian 13 or Ubuntu 24.04, 4+ cores, 8+ GB RAM.

The package-installation, binary-copy, working-directory, and service-unit
commands below are examples. Substitute operator-selected source and published
GeoIP paths consistently. Paths labeled as built-in defaults above come from
the implementation; placeholders are not project requirements.

```sh
apt update
apt install -y nginx iputils-ping traceroute dnsutils bgpdump fail2ban ufw

mkdir -p /var/lib/looking-glass
cp looking-glass /usr/local/bin/looking-glass
```

### BGP data

Obtain a full RIB snapshot from the selected provider, transfer it to the
conversion system if necessary, and run the operator-supplied `mrt2json` tool:

```sh
mrt2json <bgp-source-dir>/latest-bview.gz <bgp-json-path>
```

Conversion takes 10–15 minutes and produces a ~260 MB JSON file. The Master
consumes only the JSON selected by `BGP_DATA_PATH`; set that variable to
`<bgp-json-path>`, or use its built-in default
`/var/lib/looking-glass/bgp.json`. Replacing `latest-bview.gz` without
conversion has no runtime effect. The BGP store polls the JSON file's mtime
every five minutes and reloads a successfully updated snapshot automatically.
A normal BGP data refresh does not require a Master restart. See
[docs/bgp-data.md](docs/bgp-data.md).

### GeoIP data

The Master loads one published canonical CSV or CSV.GZ artifact from
`GEOIP_PATH`. The artifact must use this exact schema:

```text
network,country,country_code,continent,continent_code,asn,as_name,as_domain
```

`GEOIP_PATH2` is not a supported second source. A non-empty value is invalid
configuration and prevents Master startup. See
[docs/GeoIp.md](docs/GeoIp.md).

An operator with the private `geoipbuilder` tool may prepare an offline
canonical candidate from MaxMind Country, MaxMind ASN, and IPinfo Lite:

```sh
geoipbuilder \
  -country <geoip-source-dir>/GeoLite2-Country.mmdb \
  -asn <geoip-source-dir>/GeoLite2-ASN.mmdb \
  -ipinfo <geoip-source-dir>/ipinfo_lite.csv.gz \
  -output <temporary-candidate.csv.gz>
```

Choose a temporary operator-controlled output path. It must not already exist,
and its directory must not be group- or world-writable. The resulting
CSV/CSV.GZ is a non-authoritative candidate only.

Validate the candidate independently against the same three source files:

```sh
geoipbuilder \
  -country <geoip-source-dir>/GeoLite2-Country.mmdb \
  -asn <geoip-source-dir>/GeoLite2-ASN.mmdb \
  -ipinfo <geoip-source-dir>/ipinfo_lite.csv.gz \
  -candidate <temporary-candidate.csv.gz>
```

To atomically replace an offline published artifact after successful
validation, add a destination using the same CSV or CSV.GZ format:

```sh
geoipbuilder \
  -country <geoip-source-dir>/GeoLite2-Country.mmdb \
  -asn <geoip-source-dir>/GeoLite2-ASN.mmdb \
  -ipinfo <geoip-source-dir>/ipinfo_lite.csv.gz \
  -candidate <temporary-candidate.csv.gz> \
  -publish <published-canonical-geoip-path>
```

The publication directory must not be group- or world-writable. The published
path must not alias any source or the candidate, including through a symbolic
link or hard link. The previous published artifact remains until the atomic
rename commit. No older generation is retained afterward. These commands do
not alter the running Master. After publication, remove the temporary
candidate, set `GEOIP_PATH=<published-canonical-geoip-path>`, ensure
`GEOIP_PATH2` is unset, and restart the Master to load the artifact. GeoIP has
no hot reload.

### Reports directory (Permanent Link)

Promoted "Permanent Link" reports are written as one JSON file per report under `REPORTS_DIR`. The master creates this directory itself at startup if it doesn't exist yet, but the service user still needs write access to wherever `REPORTS_DIR` points — under the default `User=root` setup below with `/var/lib/looking-glass` already created as root, this just works; for a hardened, non-root deployment, `chown` the directory (or its parent) to the service user first.

```
Environment=REPORTS_DIR=/var/lib/looking-glass/reports
```

### systemd service

This is a generic service-unit example. Replace
`<PUBLISHED_CANONICAL_GEOIP_PATH>` with the chosen published canonical CSV or
CSV.GZ path. See [Filesystem and data layout](#filesystem-and-data-layout)
for path roles and source-defined defaults.

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
Environment=GEOIP_PATH=<PUBLISHED_CANONICAL_GEOIP_PATH>
Environment=REPORTS_DIR=/var/lib/looking-glass/reports
Environment=LISTEN_ADDR=127.0.0.1:8082
Environment=AGENT_SECRET=<YOUR_SECRET>
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

The `agent` binary used below is operator-supplied private tooling, not a build
artifact available from the public source checkout.

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

`Default` below means the implementation fallback when the variable is unset;
an explicit environment value overrides it where supported.

| Binary | Variable | Default | Description |
|---|---|---|---|
| master | `LISTEN_ADDR` | `127.0.0.1:8082` | TCP bind address |
| master | `BGP_DATA_PATH` | `/var/lib/looking-glass/bgp.json` | Authoritative generated BGP JSON path |
| master | `GEOIP_PATH` | `/var/lib/looking-glass/ipinfo_lite.csv.gz` | One published canonical CSV or CSV.GZ artifact; set explicitly to the operator-selected publication path |
| master | `GEOIP_PATH2` | Must be unset | Unsupported; a non-empty value stops startup |
| master | `REPORTS_DIR` | `/var/lib/looking-glass/reports` | Permanent Link report directory; service user needs write access |
| master | `LOOKING_GLASS_RESOLVERS` | Built-in list | DNS resolvers for `/api/dig` |
| master | `AGENT_SECRET` | Required | Authenticates Master requests to agents; the value is secret |
