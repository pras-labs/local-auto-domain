# PRD 001: Core Daemon and Routing

**Status**: Done  
**Date**: 2026-05-07

---

## Problem

Developers who use `ssh -L` or `kubectl port-forward` to access remote services face two friction points:

1. **No human-readable addresses.** Forwards land on `127.0.0.1:NNNNN`. Remembering which port maps to which service is error-prone, especially with multiple concurrent forwards.

2. **Port collisions.** Two forwards to the same remote service port (e.g., two Postgres databases) cannot both bind `:5432` on the same local IP. Users must pick arbitrary ports and track the mapping manually.

---

## Goals

| Goal | Metric |
| ---- | ------ |
| Resolvable domain per port-forward | Within 2s of process start |
| Zero port collision | Multiple services on same remote port independently addressable |
| Zero runtime privilege | Daemon runs as current user after `lad setup` |
| Automatic cleanup | Domain removed within 2s of process exit |
| Single binary | No runtime Python, Node, or shell script dependencies |

### Non-Goals

- Intercepting or proxying traffic for inspection
- Working without dnsmasq (no embedded DNS server)
- Supporting Windows

---

## Users

**Primary**: Backend/platform engineers who use SSH port-forwarding or `kubectl port-forward` to access databases, internal web services, or Kubernetes workloads.

**Without lad**:
```
ssh -fNL 127.0.0.1:5433:db.internal:5432 bastion
ssh -fNL 127.0.0.1:8181:app.internal:80  bastion
# 30 minutes later: which port was postgres again?
```

**With lad**:
```
ssh -fNL 127.0.0.1:5433:db.internal:5432 bastion
ssh -fNL 127.0.0.1:8181:app.internal:80  bastion
# auto-detected within 2s:
psql -h db-internal-pgsql.tunnel.test
curl http://app-internal-http.tunnel.test:8080
```

---

## Solution

A Go daemon polls running processes every 2 seconds. For each new `ssh -L` or `kubectl port-forward` process:

1. Generate a domain name from the remote host/resource and service type
2. Allocate a unique IP from `127.0.1.0/24`
3. Configure dnsmasq to resolve that domain to that IP
4. Start a TCP proxy bound to `{uniqueIP}:{servicePort}` forwarding to `localhost:{localPort}`

When the process exits, everything is cleaned up automatically.

---

## Architecture

```
local-auto-domain/
├── cmd/local-auto-domain/    # CLI entry point (cobra)
└── internal/
    ├── scanner/              # Detect ssh/kubectl LISTEN sockets
    │   ├── scanner.go        # Interface + shared SSH/kubectl parsing
    │   ├── scanner_darwin.go # lsof-based
    │   └── scanner_linux.go  # ss + /proc-based
    ├── daemon/               # Poll loop, lifecycle orchestration
    ├── domain/               # Domain name generation and sanitization
    ├── config/               # YAML config with service port mappings
    ├── ipalloc/              # 127.0.1.1–254 IP pool
    ├── netutil/              # Loopback alias management (macOS/Linux)
    ├── dnsmasq/              # dnsmasq config file writer + setup/teardown
    ├── proxy/                # TCP proxy (bindIP:port → localhost:targetPort)
    ├── ipc/                  # Unix socket server/client (daemon ↔ CLI)
    └── service/              # launchd / systemd unit install
```

**Traffic path**:
```
curl http://app-http.tunnel.test:8080
  → mDNSResponder → /etc/resolver/test → dnsmasq → 127.0.1.X
  → proxy 127.0.1.X:8080
  → 127.0.0.1:localPort (ssh tunnel)
  → remote:80
```

---

## Domain Naming

Pattern: `{identifier}-{service}.tunnel.test`

**Identifier** (priority order):
1. Override from config (`lad set <port> <name>`)
2. SSH: remote host with dots replaced by dashes (`10.0.0.2` → `10-0-0-2`)
3. kubectl: resource name stripped of type prefix (`svc/myapp` → `myapp`)
4. Fallback: `port-{localPort}`

**Service name** from remote port:

| Remote port | Label |
| ----------- | ----- |
| 80          | http  |
| 443         | https |
| 5432        | pgsql |
| 3306        | mysql |
| 6379        | redis |
| 27017       | mongo |
| 6443        | k8s   |
| other       | port{N} |

**Collision**: if two active forwards produce the same domain, local port is appended: `app-http-8181.tunnel.test`.

**Sanitization**: lowercase, dots → dashes, max 63 chars per DNS label.

### TLD: `.tunnel.test`

RFC 2606 reserves `.test` for testing; it is never delegated in global DNS and is not special-cased by any OS resolver. `.tunnel.localhost` was the original choice but was rejected: macOS RFC 6761 compliance hardcodes all `*.localhost` to `127.0.0.1` in mDNSResponder before consulting `/etc/resolver/`, so dnsmasq never receives those queries. See [ADR-009](../../adr/009-tld-change-to-tunnel-test.md).

---

## Routing Model

Each port-forward gets a **unique loopback IP** from `127.0.1.0/24`. Multiple forwards to the same remote service port can each bind the service's canonical port because they are on different IPs.

```
ssh -L 127.0.0.1:5433:db1.internal:5432  →  db1-internal-pgsql.tunnel.test  →  127.0.1.1:5432
ssh -L 127.0.0.1:5434:db2.internal:5432  →  db2-internal-pgsql.tunnel.test  →  127.0.1.2:5432
```

**Linux**: `127.0.0.0/8` routes to `lo` by default; no setup needed.  
**macOS**: `lad setup` creates `ifconfig lo0 alias 127.0.1.{1..100}` and installs a LaunchDaemon so aliases survive reboot.

---

## DNS Infrastructure

### macOS

- `/etc/resolver/test` contains `nameserver 127.0.0.1` and `port 5300`
- dnsmasq listens on port 5300 (non-privileged) and runs as a user-level LaunchAgent
- `lad setup` uses `launchctl asuser <uid>` to start dnsmasq within the user's GUI session
- mDNSResponder routes all `*.test` queries to `127.0.0.1:5300`
- `pkill -HUP dnsmasq` works without sudo because dnsmasq runs as the current user

### Linux

- dnsmasq binds port 53 (requires root); runs as a system service, drops to `nobody`
- `lad setup` writes `/etc/sudoers.d/local-auto-domain`: `NOPASSWD: /usr/bin/systemctl reload dnsmasq`
- `systemctl reload dnsmasq` sends SIGHUP via systemd — correct even after privilege drop

### Dynamic reload mechanism

dnsmasq does **not** re-read `conf-dir` entries on SIGHUP — those are startup-only. The daemon maintains a single `hosts` file registered via `addn-hosts` in `/etc/hosts` format. On every domain add/remove, the daemon rewrites this file and signals dnsmasq — live within 2 seconds, no restart required.

Per-port `port-N.conf` state files exist in the drop-in dir for daemon crash/restart recovery only; dnsmasq does not read them at runtime.

dnsmasq is pinned to `127.0.0.1` via `listen-address=127.0.0.1` + `bind-interfaces` to prevent conflict with systemd-resolved's stub listener on `127.0.0.53:53`.

### Note on DNS verification tools

`host`, `dig`, `nslookup` bypass mDNSResponder / systemd-resolved per-domain routing and return NXDOMAIN for `.test` — expected. Use `dns-sd -q <name> A` (macOS) or `getent hosts <name>` (Linux) to verify resolution.

---

## CLI Interface

```
lad daemon              # Start daemon (foreground)
lad setup               # One-time: install dnsmasq, resolver (requires sudo)
lad uninstall           # Full uninstall (requires sudo)
lad list                # List active port-forward → domain mappings
lad status              # Daemon running? Service installed? Active count?
lad set <port> <name>   # Override domain identifier for a port
lad unset <port>        # Remove override
lad install-service     # Register daemon as login service (launchd / systemd --user)
lad uninstall-service   # Remove login service
lad version             # Print version
```

`lad list` output:

```
PORT   DOMAIN                         IP          PROXY   TOOL      PID    SINCE
8080   argocd-server-http.tunnel.test 127.0.1.2   :8080   kubectl   5678   12m ago
5433   10-0-0-4-pgsql.tunnel.test     127.0.1.3   :5432   ssh       9012   1m ago
```

---

## Configuration

Location: `~/.config/local-auto-domain/config.yaml` (Linux) / `~/Library/Application Support/local-auto-domain/config.yaml` (macOS)

```yaml
poll_interval: 2s
tld: tunnel.test

overrides:
  8181: myapp
  5433: prod-db

service_ports:
  http: 8080
  https: 8443
  pgsql: 5432
  mysql: 3306
  redis: 6379
  mongo: 27017
  k8s: 6443
```

---

## IPC

Daemon exposes `GET /state` over a Unix socket at `~/.local/share/local-auto-domain/daemon.sock`. CLI reads state without re-scanning.

```go
type Entry struct {
    Port       int
    RemoteHost string
    RemotePort int
    Resource   string
    IP         string
    ProxyPort  int
    Domain     string
    Tool       string
    TLS        bool
    PID        int
    Since      time.Time
    Cmdline    string
}
```

---

## Privilege Model

| Operation | Privilege |
| --------- | --------- |
| `lad setup` | sudo (once) |
| `lad daemon` | current user |
| `lad install-service` | current user |
| `lad uninstall` | sudo (loopback aliases on macOS) |
| Runtime dnsmasq updates (macOS) | current user — dnsmasq runs as user LaunchAgent on port 5300 |
| Runtime dnsmasq updates (Linux) | `sudo systemctl reload dnsmasq` via NOPASSWD sudoers rule |
| Loopback aliases (macOS) | created by root LaunchDaemon at boot; no runtime sudo |

---

## Platform Support

|                     | macOS                              | Linux                              |
| ------------------- | ---------------------------------- | ---------------------------------- |
| Socket detection    | `lsof`                             | `ss` + `/proc`                     |
| DNS resolver config | `/etc/resolver/test` (port 5300)   | systemd-networkd + resolved (see PRD-003) |
| Loopback aliases    | `ifconfig lo0 alias` via setup     | not needed (127.0.0.0/8 routable)  |
| Service manager     | launchd (LaunchAgents)             | systemd --user                     |

---

## SSH Flag Compatibility

All common `ssh -L` forms are detected and parsed:

```
ssh -L 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -NL 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -fNL 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -fNLW 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -L8181:10.0.0.2:80 user@host
ssh -L 8181:10.0.0.2:80 user@host
```

Detection regex: `/-[a-zA-Z]*L/` (matches any combined flag group containing L).

---

## kubectl Compatibility

```
kubectl port-forward svc/myapp 8080:80
kubectl -n monitoring port-forward pod/grafana-abc 3000:3000
kubectl port-forward svc/postgres 5432:5432
kubectl port-forward deploy/api 8080:8080
```

Resource name extracted by stripping `svc/`, `pod/`, `deploy/`, `deployment/`, `replicaset/`, `rs/`, `statefulset/`, `sts/`, `daemonset/`, `ds/` prefixes.

---

## Data Storage

| Path | Content |
| ---- | ------- |
| `~/.local/share/local-auto-domain/` | Runtime data directory |
| `…/daemon.sock` | Unix socket for IPC |
| `/etc/dnsmasq.d/local-auto-domain/hosts` | addn-hosts file — re-read by dnsmasq on SIGHUP |
| `/etc/dnsmasq.d/local-auto-domain/port-N.conf` | Per-port state files (daemon restart recovery) |
| `/etc/resolver/test` | macOS resolver routing |
| `~/.config/local-auto-domain/config.yaml` | User configuration |
| `~/Library/LaunchAgents/com.pras-labs.local-auto-domain.plist` | macOS login service |
| `~/.config/systemd/user/local-auto-domain.service` | Linux login service |
| `/Library/LaunchDaemons/com.pras-labs.local-auto-domain-lo.plist` | macOS boot loopback aliases |

---

## Decision Records

| ADR | Decision |
| --- | -------- |
| [001](../../adr/001-use-tunnel-localhost-tld.md) | Original TLD choice (superseded by ADR-009) |
| [002](../../adr/002-unique-loopback-ip-per-forward.md) | Unique 127.0.1.X IP per port-forward |
| [003](../../adr/003-dnsmasq-drop-in-files-sighup.md) | Per-domain dnsmasq conf files + SIGHUP |
| [004](../../adr/004-poll-based-process-detection.md) | Poll-based detection over kernel events |
| [005](../../adr/005-unix-socket-http-ipc.md) | Unix socket HTTP for daemon ↔ CLI IPC |
| [006](../../adr/006-macos-loopback-alias-strategy.md) | macOS loopback alias creation strategy |
| [008](../../adr/008-identifier-service-domain-naming.md) | `{identifier}-{service}.tld` naming scheme |
| [009](../../adr/009-tld-change-to-tunnel-test.md) | TLD change from `.tunnel.localhost` to `.tunnel.test` |
| [010](../../adr/010-dnsmasq-addn-hosts-reload.md) | dnsmasq dynamic reload via addn-hosts; macOS port 5300 + user LaunchAgent; Linux sudoers |
