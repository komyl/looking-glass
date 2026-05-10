# looking-glass

A self-hosted network looking glass written in Go. Zero external runtime dependencies. Designed for ISPs, data centers, and network operators who need full infrastructure autonomy.

Probes are distributed across multiple measurement nodes. The master orchestrates, the agents execute. BGP routing data is loaded from local MRT dumps and enriched with GeoIP and AS operator information at query time.

## Quick start

```sh
go build -ldflags="-s -w" -trimpath -o looking-glass .
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o agent ./cmd/agent/
```

See [INSTALL.md](INSTALL.md) for full deployment instructions.

## Features

- Multi-node ping — all nodes probed in parallel, results in a single table
- Traceroute — user selects source node. Output streamed in real time via SSE. Each hop IP is enriched with ASN and operator name from the ipinfo dataset.
- Port check — TCP connect with open/closed/filtered status
- DNS lookup — A, AAAA, MX, NS, TXT, CNAME, SOA, PTR
- SSL inspection — certificate details, validity window, SAN list
- BGP route lookup — IP, prefix, ASN; enriched with GeoIP and AS operator names
- AS path rendered as directed chain: `AS34549 (meerfarbig) → AS15169 (Google LLC)`

## Source layout

```
main.go               master entry point
cmd/agent/            agent binary
cmd/mrt2json/         offline MRT-to-JSON converter
internal/bgp/         BGP table: radix trie, JSON loader, hot-reload
internal/geoip/       ipinfo CSV loader, radix trie, ASN index
internal/handler/     HTTP handlers and agent proxy
internal/nodes/       node registry
internal/ratelimit/   token bucket rate limiter
internal/validator/   input sanitization
web/index.html        UI, embedded into binary at build time
```

## Requirements

- Go 1.22+
- Master node: 4+ cores, 8+ GB RAM
- Agent nodes: 2+ cores, 4+ GB RAM
- Debian 13 or Ubuntu 24.04

## Documentation

- [INSTALL.md](INSTALL.md) — build, deploy, nginx, systemd, fail2ban
- [docs/architecture.md](docs/architecture.md) — system design and decisions
- [docs/api.md](docs/api.md) — HTTP API reference
- [docs/bgp-data.md](docs/bgp-data.md) — MRT format, conversion, update process
- [docs/geoip.md](docs/geoip.md) — ipinfo CSV setup
- [CONTRIBUTING.md](CONTRIBUTING.md) — adding nodes, contributing patches
- [CHANGELOG](CHANGELOG) — version history
- [SECURITY.md](SECURITY.md) — reporting vulnerabilities

## License

GNU Affero General Public License v3.0 — see [LICENSE](LICENSE).
