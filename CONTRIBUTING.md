# Contributing

## Adding a measurement node

1. Provision a host running Debian 13 or Ubuntu 24.04, minimum 2 cores and 4 GB RAM.

2. Install required packages:
   ```sh
   apt update && apt install -y iputils-ping traceroute
   ```

3. Copy the agent binary and install the systemd service with `LISTEN_ADDR=0.0.0.0:9090` and the shared `AGENT_SECRET`. See [INSTALL.md](INSTALL.md) for the full service unit.

4. Restrict agent port to master IP only:
   ```sh
   ufw allow from <MASTER_IP> to any port 9090
   ufw reload
   ```

5. Verify connectivity from the master:
   ```sh
   curl -s -H "X-Agent-Secret: <SECRET>" http://<NODE_IP>:9090/health
   ```

6. Register the node in `internal/nodes/nodes.go`:
   ```go
   {
       ID:       "nodeid",      // lowercase alphanumeric, URL-safe, unique
       Name:     "City — ISP",
       Location: "City",
       ISP:      "ISP Name",
       IP:       "<NODE_IP>",   // shown publicly in /api/nodes
       URL:      "http://<NODE_IP>:9090", // internal only, never exposed
   },
   ```

7. Rebuild the master and restart:
   ```sh
   go build -ldflags="-s -w" -trimpath -o looking-glass .
   systemctl restart looking-glass
   ```

## Rotating the agent secret

Update `AGENT_SECRET` in every node's service file and the `Secret` constant in `internal/nodes/nodes.go`. Rebuild the master. Restart all services. There is no grace period — old and new secrets cannot coexist.

## Code style

- Standard Go formatting. Run `gofmt` before committing.
- No external Go dependencies. The project uses stdlib only.
- Any change to `web/index.html` requires a master rebuild — the file is embedded at compile time.
- Keep commit messages in the form `component: short description of what changed`.