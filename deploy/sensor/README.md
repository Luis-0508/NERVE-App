# NERVE Linux capture sensor

This is a **separate native Linux Docker Engine deployment**. Do not run it on
Windows Docker Desktop or WSL. Setup creates the complete bundle in `.nerve/sensor`,
including two unique secret files and the management address/NIC in `.env`.

1. Use a dedicated Linux host or a VM with a suitable physical NIC passed through.
   Identify the observation point with `ip -br link`. A normal switch port sees
   this host's traffic and delivered broadcast/multicast, not other devices' unicast.
2. Copy the generated bundle securely to that host. Protect its directory (`chmod
   700 .`) and secret files (`chmod 600 *.password .env`). Do not copy secrets into Git.
3. Allow TCP 3001 on the selected management address only from the NERVE API's
   actual egress address. HTTP Basic authentication is unencrypted: use a trusted
   management network or VPN, or put TLS in front and update NERVE's source URL.
4. In the bundle: `docker compose config --quiet`, then
   `docker compose up -d --build`, then `docker compose ps`.
5. Start NERVE on Windows with `start.bat`. In its repository run
   `docker compose exec api /app/nerve-server traffic-check`.

The service starts private Unix-socket Redis, installs the generated administrator
and read-only `nerve` accounts **before** opening HTTP, and then starts ntopng
Community. No default passwords, public Redis port, commercial trial, packet dump
or flow-history export is configured. ntopng preferences and its normal local
statistics persist in `sensor-state`. Secrets are authoritative on every restart;
changing an account in the ntopng UI alone is not a persistent password rotation.
Admin UI: `http://<management-IP>:3001`, account `admin`, password from the protected
`admin.password` file. Do not paste that password into logs or support tickets.

Only one capture NIC is configured. NERVE resolves its numeric ID via REST v2.
With multiple interfaces use an explicit ID in NERVE. Interface selection and
connectivity do not establish coverage. For wider monitoring configure SPAN/TAP
or capture at a gateway; mirror both directions and avoid oversubscribing the mirror
port. Traffic that does not traverse that observation point remains invisible.
The management API traffic is excluded from capture by BPF.

Verify using real controlled traffic: download a file on the Linux host, then on
Windows, then on another LAN device. Compare ntopng's interface counters/active
flows with the intended topology, including both directions. Without mirroring,
the latter two must not be claimed as monitored. A router's WAN side usually loses
private device identity through NAT. NERVE maps external flows only; local-to-local
flows can be visible in ntopng without appearing on the world map.

The image pins the official `ntop/ntopng` Linux amd64 image by digest. Account bootstrapping follows
the upstream Redis schema; upgrades require an authenticated healthcheck and the
above real capture acceptance test. Docker builds, account bootstrap, read-only REST
login, default-password rejection and a real container HTTPS flow through the NERVE
API were validated on Docker Desktop. Physical Linux NIC/SPAN capture remains untested.
Kernel checks reject known Desktop/WSL kernels, but do not prove physical coverage.

`docker compose stop` preserves data. Never use `down -v` unless intentionally
deleting sensor history. This deployment never accesses the NERVE PostgreSQL volume.
