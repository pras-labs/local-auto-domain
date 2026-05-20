# Product Requirements Document: local-auto-domain

**Version**: 1.0  
**Date**: 2026-05-07

---

## Problem

Developers who use `ssh -L` or `kubectl port-forward` to access remote services face three friction points:

1. **No human-readable addresses.** Forwards land on `127.0.0.1:NNNNN`. Remembering which port is which service is error-prone, especially across multiple concurrent forwards.

2. **Port collisions.** Two forwards to the same service port (e.g., two Postgres databases) cannot both bind `:5432` on the same IP. Users must pick arbitrary ports and track the mapping manually.

3. **HTTPS is broken.** When a forwarded service uses TLS, the remote certificate's CN/SAN won't match the local domain. Every browser and curl request fails certificate validation unless the user passes `-k`.

---

## Solution

A Go daemon polls running processes every 2 seconds. For each `ssh -L` or `kubectl port-forward` detected:

1. Generate a domain name from the remote host/resource and service type
2. Allocate a unique IP from `127.0.1.0/24`
3. Configure dnsmasq to resolve that domain to that IP
4. Start a TCP proxy on `{uniqueIP}:{servicePort}` forwarding to `localhost:{localPort}`
5. For HTTPS services: proxy terminates TLS using a locally-trusted wildcard cert

When the process exits, all of the above are cleaned up automatically.

---

## Architecture

```
local-auto-domain/
├── cmd/local-auto-domain/    # CLI entry point (cobra)
└── internal/
    ├── scanner/              # Detect ssh/kubectl LISTEN sockets
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

---

## Feature PRDs

All features are implemented. Historical PRDs are in `prds/done/`:

| PRD | Feature | Status |
| --- | ------- | ------ |
| [001-core.md](prds/done/001-core.md) | Core daemon, process detection, domain naming, unique IP routing, TCP proxy, dnsmasq setup, CLI, IPC, service management | Done |
| [002-https-tls.md](prds/done/002-https-tls.md) | HTTPS/TLS termination with local CA wildcard cert | Done |
| [003-linux-split-dns.md](prds/done/003-linux-split-dns.md) | Linux split-DNS via lad-dns dummy interface | Done |

---

## Decision Records

Detailed rationale in `docs/adr/`:

| ADR | Decision |
| --- | -------- |
| [001](adr/001-use-tunnel-localhost-tld.md) | Original TLD choice (superseded) |
| [002](adr/002-unique-loopback-ip-per-forward.md) | Unique 127.0.1.X IP per port-forward |
| [003](adr/003-dnsmasq-drop-in-files-sighup.md) | Per-domain dnsmasq conf files + SIGHUP |
| [004](adr/004-poll-based-process-detection.md) | Poll-based detection over kernel events |
| [005](adr/005-unix-socket-http-ipc.md) | Unix socket HTTP for daemon ↔ CLI IPC |
| [006](adr/006-macos-loopback-alias-strategy.md) | macOS loopback alias creation strategy |
| [007](adr/007-tls-termination-local-ca.md) | TLS termination with local CA + wildcard cert |
| [008](adr/008-identifier-service-domain-naming.md) | `{identifier}-{service}.tld` naming scheme |
| [009](adr/009-tld-change-to-tunnel-test.md) | TLD change from `.tunnel.localhost` to `.tunnel.test` |
| [010](adr/010-dnsmasq-addn-hosts-reload.md) | dnsmasq dynamic reload via addn-hosts; macOS port 5300 + user LaunchAgent; Linux sudoers |
| [011](adr/011-linux-split-dns-lad-dns-dummy-interface.md) | Linux split-DNS via lad-dns dummy interface |
