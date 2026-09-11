# Contributing

The public checkout contains the Master/runtime source. The measurement agent
is private operator-side tooling; its deployment interface is documented here,
but its implementation is not distributed under `cmd/` in the public
repository.

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
       IP:       "<NODE_IP>",   // internal only, never exposed
       URL:      "http://<NODE_IP>:9090", // internal only, never exposed
   },
   ```

7. Rebuild the master and restart:
   ```sh
   go build -ldflags="-s -w" -trimpath -o looking-glass .
   systemctl restart looking-glass
   ```

## Rotating the agent secret

Update `AGENT_SECRET` for the Master and every agent in a coordinated manner,
then restart the affected services so they read the new environment value.
There is no source-code secret constant to edit, and a Master rebuild is not
required solely for secret rotation. There is no grace period or dual-secret
support — old and new secrets cannot coexist.

## Code style

- Standard Go formatting. Run `gofmt` before committing.
- No external Go dependencies. The project uses stdlib only.
- Any change to files inside the `web/` directory requires a master rebuild because the directory is embedded at compile time.
- Keep commit messages in the form `component: short description of what changed`.
