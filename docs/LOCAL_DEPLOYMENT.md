# Running NERVE locally

Details for the Windows quick start and Docker Compose setup in the [README](../README.md):
secrets, transport and LAN access, stopping, runtime checks and local discovery.

## Secrets, transport and LAN access

Generated secrets stay in ignored local configuration; no batch file contains them. The sensor token is additionally copied to its ACL-protected service configuration. When an existing `.env` is reconfigured, `setup.bat` keeps its current database password because PostgreSQL does not reapply `POSTGRES_PASSWORD` to an initialized volume. Discovery and capture authenticate separately and never receive browser sessions or database credentials. Management agents use their own per-host keys (see [Agent setup](AGENT_SETUP.md)).

**Transport.** The local quick start serves NERVE over plain HTTP. Browsers, the discovery agent and management agents talk to Core over whatever URL they are given, so traffic is encrypted only if you put NERVE behind an HTTPS reverse proxy with a trusted certificate. Management agents authenticate every request with their own key and verify Core's response signatures, so commands cannot be forged on the network, but on plain HTTP their results and inventory data are readable by anyone on the path. Use HTTPS for agents outside a trusted network.

Use `http://localhost:3000` unless `NERVE_WEB_PORT` was changed in `.env`. When a reliable active IPv4 address is available, the script also prints a LAN URL such as `http://192.168.1.25:3000`. Docker publishes the web proxy on all host interfaces. Windows Firewall and the local network must permit inbound access to the configured port.

The browser API remains reachable through the web reverse proxy. Native sensor ingestion uses a separate authenticated endpoint published only on host loopback; it is not exposed through Nginx. For LAN access, Nginx forwards the original host and scheme and the API accepts only an `Origin` that matches that trusted proxy origin or an explicitly configured `NERVE_ALLOWED_ORIGIN`; origin checks are not disabled. Keep `NERVE_COOKIE_SECURE=false` for plain local HTTP and set it to `true` when NERVE is served through HTTPS.

## Stopping NERVE

To stop NERVE, including the host discovery agent and traffic sensor, while preserving PostgreSQL data, run `stop.bat`. The Compose-only equivalent is:

```text
docker compose down
```

Use `docker compose down -v` only when the PostgreSQL volume and all local NERVE data should be deleted.

## Runtime verification

The following checks all use the same address exposed by the reverse proxy:

- `/` returns the web interface.
- `/api/v1` returns the NERVE API name, version, and `status: ok`.
- `/health/live` confirms that the Go process is running.
- `/health/ready` returns HTTP 200 only when the API can reach PostgreSQL.
- `/health/discovery` returns HTTP 200 only while a local discovery agent is reporting heartbeats.
- `/api/v1/status` reports both overall `status: ok` and `database.status: ok`.

`docker compose ps` shows the container state. Use `docker compose logs --tail 100 postgres api web` when a service is not ready. The start script prints both automatically after a failed startup and does not open a browser in that case.

## Local network discovery

Open **Devices**, enter a canonical subnet such as `192.168.178.0/24`, save it, and select **Scan now**. Only users with the `discovery.manage` permission (administrators by default) can configure or start scans. Target changes, scan requests and cancellations are recorded in the audit log; scan results reported by the discovery agent are recorded as observations and events, not audit entries. The page shows whether the host agent is online, the current scan state, reachable/total counts when available, and recent scan errors. Results are written into the normalized device, interface, address, identity, observation, event, and discovery tables; the inventory refreshes when a scan completes.

On Windows with Docker Desktop, discovery intentionally runs as a native host process. Docker Desktop runs Linux containers inside a VM, and its host-network feature exposes TCP/UDP at layer 4 rather than the host's layer-2 interfaces. Running ARP/NDP discovery inside the API container would therefore inspect the wrong network namespace. `start.bat` cross-compiles the Go agent in Docker, writes the generated binary and logs beneath ignored `.nerve/`, and starts it with ordinary user privileges. ICMP uses the operating-system `ping` command; the agent then reads the host ARP/NDP neighbor cache, performs bounded parallel reverse DNS, and uses bounded TCP connection attempts as a fallback.

For a later Linux server or VM, build `Dockerfile.agent` with `TARGETOS=linux` and run the resulting `nerve-agent` directly on that host with `NERVE_AGENT_API_URL` and `NERVE_DISCOVERY_AGENT_TOKEN` set. This keeps discovery in the target network namespace without granting raw-socket or Docker-socket privileges to the Core container.

If a browser tab remains blank after rebuilding an older checkout, reload it without cache (`Ctrl+F5`). The production build includes `npm run test:runtime`, which verifies that the exported application shell, referenced static assets, and Nginx content-security policy are compatible with the Next.js bootstrap runtime.
