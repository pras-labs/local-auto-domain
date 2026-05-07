# Product Requirements Document: local-auto-domain

**Version**: 1.0
**Date**: 2026-05-07
**Status**: Current

---

## 1. Problem Statement

Developers who use `ssh -L` or `kubectl port-forward` to access remote services face three friction points:

1. **No human-readable addresses.** Forwards land on `127.0.0.1:NNNNN`. Remembering which port is which service is error-prone, especially across multiple concurrent forwards.

2. **Port collisions.** Two forwards to the same service port (e.g., two Postgres databases) cannot both bind `:5432` on the same IP. Users must pick arbitrary ports and track the mapping manually.

3. **HTTPS is broken.** When a forwarded service uses TLS, the remote certificate's CN/SAN won't match `localhost` or `127.0.0.1`. Every browser and curl request fails certificate validation unless the user passes `-k`, which trains insecure habits.

These are solvable problems. The tooling to resolve them (dnsmasq, loopback aliases, local CAs) exists on every developer machine. It just needs to be wired together automatically.

---

## 2. Goals

| Goal                               | Metric                                                                |
| ---------------------------------- | --------------------------------------------------------------------- |
| Resolvable domain per port-forward | Within 2s of process start                                            |
| Zero port collision                | Multiple services on same remote port independently addressable       |
| Trusted HTTPS                      | `curl https://<domain>` returns 200 without `-k` after one-time setup |
| Zero runtime privilege             | Daemon runs as current user after `lad setup`                         |
| Automatic cleanup                  | Domain removed within 2s of process exit                              |
| Single binary                      | No runtime Python, Node, or shell script dependencies                 |

### Non-Goals

- Intercepting or proxying traffic for inspection (not a debugging proxy)
- Working without dnsmasq (no embedded DNS server)
- Supporting Windows
- Public certificate issuance (only local CA; no ACME/Let's Encrypt — impossible for private TLDs)

---

## 3. Users

**Primary**: Backend/platform engineers who use SSH port-forwarding or `kubectl port-forward` to access databases, internal web services, or Kubernetes workloads from their local machine.

**Typical workflow without lad**:

```
ssh -fNL 127.0.0.1:5433:db.internal:5432 bastion
ssh -fNL 127.0.0.1:8181:app.internal:80  bastion
# 30 minutes later: which port was postgres again?
# psql -h localhost -p 5433   (had to look it up)
```

**Typical workflow with lad**:

```
ssh -fNL 127.0.0.1:5433:db.internal:5432 bastion
ssh -fNL 127.0.0.1:8181:app.internal:80  bastion
# auto-detected within 2s:
psql -h db-internal-pgsql.tunnel.test
curl http://app-internal-http.tunnel.test:8080
```

---

## 4. Solution Overview

A Go daemon polls running processes every 2 seconds. For each new `ssh -L` or `kubectl port-forward` process detected:

1. Generate a domain name from the remote host/resource and service type
2. Allocate a unique IP from `127.0.1.0/24`
3. Configure dnsmasq to resolve that domain to that IP
4. Start a TCP proxy bound to `{uniqueIP}:{servicePort}` forwarding to `localhost:{localPort}`
5. For HTTPS services: proxy terminates TLS using a locally-trusted wildcard cert

When the forward process exits, all of the above are cleaned up automatically.

---

## 5. Architecture

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
    ├── proxy/                # TCP proxy; TLS mode for HTTPS
    ├── tlscert/              # Local CA + wildcard cert; system trust install
    ├── ipc/                  # Unix socket server/client (daemon ↔ CLI)
    └── service/              # launchd / systemd unit install
```

### Traffic path (HTTP)

```
curl http://app-http.tunnel.test:8080
  → mDNSResponder → /etc/resolver/test → dnsmasq → 127.0.1.X
  → proxy 127.0.1.X:8080
  → 127.0.0.1:localPort (ssh tunnel)
  → remote:80
```

### Traffic path (HTTPS)

```
curl https://app-https.tunnel.test:8443
  → mDNSResponder → dnsmasq → 127.0.1.X
  → TLS handshake (*.tunnel.test cert, trusted) → proxy 127.0.1.X:8443
  → TLS upstream (InsecureSkipVerify) → 127.0.0.1:localPort
  → SSH tunnel → remote:443
```

---

## 6. Domain Naming

### Pattern

```
{identifier}-{service}.tunnel.test
```

### Identifier (priority order)

1. Override from config (`lad set <port> <name>`)
2. SSH: remote host with dots replaced by dashes (`10.0.0.2` → `10-0-0-2`)
3. kubectl: resource name stripped of type prefix (`svc/myapp` → `myapp`)
4. Fallback: `port-{localPort}`

### Service name from remote port

| Remote port | Label   |
| ----------- | ------- |
| 80          | http    |
| 443         | https   |
| 5432        | pgsql   |
| 3306        | mysql   |
| 6379        | redis   |
| 27017       | mongo   |
| 6443        | k8s     |
| other       | port{N} |

### Collision handling

If two active forwards produce the same domain, local port number is appended as suffix: `app-http-8181.tunnel.test`.

### Sanitization

All labels: lowercase, dots → dashes, max 63 chars per DNS label.

---

## 7. Routing Model

Each port-forward gets a **unique loopback IP** from `127.0.1.0/24`. Multiple forwards to the same remote service port can each bind their service's canonical port (e.g., both on `:5432`) because they're on different IPs.

```
ssh -L 127.0.0.1:5433:db1.internal:5432  →  db1-internal-pgsql.tunnel.test  →  127.0.1.1:5432
ssh -L 127.0.0.1:5434:db2.internal:5432  →  db2-internal-pgsql.tunnel.test  →  127.0.1.2:5432
```

**Linux**: `127.0.0.0/8` routes to `lo` by default; no setup needed.  
**macOS**: `lad setup` creates `ifconfig lo0 alias 127.0.1.{1..100}` and installs a LaunchDaemon so aliases survive reboot.

---

## 8. TLS Support

### Cert generation (during `lad setup`)

- ECDSA P-256 local CA (`CN=local-auto-domain CA`, 10yr)
- Wildcard leaf cert (`CN=*.tunnel.test`, `SANs: *.tunnel.test, tunnel.test`, 2yr)
- Go stdlib only — no mkcert or external tools
- Files stored in `~/.local/share/local-auto-domain/` at `0600`

### Trust installation

- **macOS**: `security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain` → trusted by Safari, Chrome, `/usr/bin/curl`
- **Linux**: copy to system CA bundle + `update-ca-certificates`
- **Homebrew curl / OpenSSL-based tools**: require `CURL_CA_BUNDLE=~/.local/share/local-auto-domain/ca.crt` (printed by `lad setup`)

### Proxy behavior for HTTPS forwards

When a forward targets remote port 443 or 8443:

- Listener uses `tls.NewListener` with the wildcard cert → client sees trusted cert
- Upstream uses `tls.Dial(InsecureSkipVerify: true)` → connects through SSH tunnel regardless of remote hostname

### Graceful degradation

If `lad setup` has not been run (no cert on disk), HTTPS forwards fall back to plain TCP passthrough. `curl -k` still works; no daemon crash.

### Cert rotation

`LoadCert` rejects certs expiring within 30 days, missing `AuthorityKeyId`, or with SANs not including `*.tunnel.test`. Any rejection triggers automatic regeneration on next `EnsureCert` call.

---

## 9. DNS Infrastructure

### TLD choice: `.tunnel.test`

RFC 2606 reserves `.test` for testing; it is never delegated in global DNS and is not special-cased by any OS resolver. See [ADR-009](adr/009-tld-change-to-tunnel-test.md) for the full decision record, including why `.tunnel.localhost` was rejected (macOS RFC 6761 compliance hardcodes all `*.localhost` to `127.0.0.1`).

### macOS

- `/etc/resolver/test` contains `nameserver 127.0.0.1`
- mDNSResponder routes all `*.test` queries to dnsmasq
- Per-domain conf files: `/opt/homebrew/etc/dnsmasq.d/local-auto-domain/port-{N}.conf`
- dnsmasq reloaded via `SIGHUP` on each add/remove (no restart)

### Linux

- systemd-resolved drop-in: `Domains=~tunnel.test`, `DNS=127.0.0.1`
- Per-domain conf files: `/etc/dnsmasq.d/local-auto-domain/port-{N}.conf`

### Note on CLI DNS tools

`host`, `dig`, `nslookup` query `/etc/resolv.conf` (external resolver) and bypass mDNSResponder. They will return NXDOMAIN for `.test` — this is expected. `curl`, browsers, and most applications use mDNSResponder and resolve correctly.

---

## 10. CLI Interface

```
lad daemon              # Start daemon (foreground; signal to stop)
lad setup               # One-time: install dnsmasq, resolver, CA (may require sudo)
lad uninstall           # Full uninstall: reverses all setup steps (requires sudo for CA/loopback)
lad list                # List active port-forward → domain mappings
lad status              # Daemon running? Service installed? Active count?
lad set <port> <name>   # Override domain identifier for a port
lad unset <port>        # Remove override
lad ca-cert             # Print path to local CA cert
lad install-service     # Register daemon as login service (launchd / systemd --user)
lad uninstall-service   # Remove login service
lad version             # Print version
```

### `lad list` output

```
PORT   DOMAIN                              IP          PROXY   TLS   TOOL      PID    SINCE
8444   10-0-0-5-https.tunnel.test          127.0.1.1   :8443   yes   ssh       1234   5m ago
8080   argocd-server-http.tunnel.test      127.0.1.2   :8080   no    kubectl   5678   12m ago
5433   10-0-0-4-pgsql.tunnel.test          127.0.1.3   :5432   no    ssh       9012   1m ago
```

---

## 11. Configuration

**Location**: `~/.config/local-auto-domain/config.yaml` (Linux) / `~/Library/Application Support/local-auto-domain/config.yaml` (macOS)

```yaml
poll_interval: 2s
tld: tunnel.test

# Override domain identifier per local port
overrides:
  8181: myapp
  5433: prod-db

# Proxy listen port per service type
# Change these if defaults conflict with locally running services
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

## 12. IPC

Daemon exposes `GET /state` over a Unix socket at `~/.local/share/local-auto-domain/daemon.sock`. Response is a JSON array of `Entry` objects. CLI commands read state without the daemon needing to re-scan.

```go
type Entry struct {
    Port       int       // local listen port
    RemoteHost string    // SSH: "10.0.0.2"; empty for kubectl
    RemotePort int       // original service port
    Resource   string    // kubectl resource name; empty for SSH
    IP         string    // assigned 127.0.1.X
    ProxyPort  int       // port the proxy listens on
    Domain     string    // full domain name
    Tool       string    // "ssh" | "kubectl"
    TLS        bool      // true when proxy terminates TLS
    PID        int
    Since      time.Time
    Cmdline    string
}
```

---

## 13. Privilege Model

| Operation                | Privilege                                                |
| ------------------------ | -------------------------------------------------------- |
| `lad setup`              | sudo (once)                                              |
| `lad daemon`             | current user                                             |
| `lad install-service`    | current user                                             |
| `lad uninstall`          | sudo (CA removal, loopback aliases on macOS)             |
| Runtime dnsmasq updates  | current user (dnsmasq.d dir made user-writable by setup) |
| Loopback aliases (macOS) | created by root LaunchDaemon at boot; no runtime sudo    |

---

## 14. Platform Support

|                     | macOS                          | Linux                             |
| ------------------- | ------------------------------ | --------------------------------- |
| Socket detection    | `lsof`                         | `ss` + `/proc`                    |
| DNS resolver config | `/etc/resolver/test`           | systemd-resolved split-DNS        |
| Loopback aliases    | `ifconfig lo0 alias` via setup | not needed (127.0.0.0/8 routable) |
| Service manager     | launchd (LaunchAgents)         | systemd --user                    |
| CA trust store      | system keychain (`security`)   | `update-ca-certificates`          |

---

## 15. SSH Flag Compatibility

All common `ssh -L` forms are detected and parsed:

```
ssh -L 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -NL 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -fNL 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -fNLW 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -L8181:10.0.0.2:80 user@host       # adjacent form
ssh -L 8181:10.0.0.2:80 user@host      # no bind addr
```

Detection regex: `/-[a-zA-Z]*L/` (matches any combined flag group containing L).

---

## 16. kubectl Compatibility

```
kubectl port-forward svc/myapp 8080:80
kubectl -n monitoring port-forward pod/grafana-abc 3000:3000
kubectl port-forward svc/postgres 5432:5432
kubectl port-forward deploy/api 8080:8080
```

Resource name extracted by stripping `svc/`, `pod/`, `deploy/`, `deployment/`, `replicaset/`, `rs/`, `statefulset/`, `sts/`, `daemonset/`, `ds/` prefixes.

---

## 17. Data Storage

| Path                                                              | Content                           |
| ----------------------------------------------------------------- | --------------------------------- |
| `~/.local/share/local-auto-domain/`                               | Runtime data directory            |
| `…/daemon.sock`                                                   | Unix socket for IPC               |
| `…/ca.crt` / `ca.key`                                             | Local CA cert + key (`0600`)      |
| `…/wildcard.crt` / `wildcard.key`                                 | Wildcard leaf cert + key (`0600`) |
| `/etc/dnsmasq.d/local-auto-domain/`                               | Per-domain dnsmasq conf files     |
| `/etc/resolver/test`                                              | macOS resolver routing            |
| `~/.config/local-auto-domain/config.yaml`                         | User configuration                |
| `~/Library/LaunchAgents/com.pras-labs.local-auto-domain.plist`    | macOS login service               |
| `~/.config/systemd/user/local-auto-domain.service`                | Linux login service               |
| `/Library/LaunchDaemons/com.pras-labs.local-auto-domain-lo.plist` | macOS boot loopback aliases       |

---

## 18. Decision Records

Detailed rationale for key design decisions is in `docs/adr/`:

| ADR                                                | Decision                                              |
| -------------------------------------------------- | ----------------------------------------------------- |
| [001](adr/001-use-tunnel-localhost-tld.md)         | Original TLD choice (superseded)                      |
| [002](adr/002-unique-loopback-ip-per-forward.md)   | Unique 127.0.1.X IP per port-forward                  |
| [003](adr/003-dnsmasq-drop-in-files-sighup.md)     | Per-domain dnsmasq conf files + SIGHUP                |
| [004](adr/004-poll-based-process-detection.md)     | Poll-based detection over kernel events               |
| [005](adr/005-unix-socket-http-ipc.md)             | Unix socket HTTP for daemon ↔ CLI IPC                 |
| [006](adr/006-macos-loopback-alias-strategy.md)    | macOS loopback alias creation strategy                |
| [007](adr/007-tls-termination-local-ca.md)         | TLS termination with local CA + wildcard cert         |
| [008](adr/008-identifier-service-domain-naming.md) | `{identifier}-{service}.tld` naming scheme            |
| [009](adr/009-tld-change-to-tunnel-test.md)        | TLD change from `.tunnel.localhost` to `.tunnel.test` |
