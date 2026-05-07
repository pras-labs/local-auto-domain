# ADR-003: Use dnsmasq with Per-Domain Drop-In Files and SIGHUP Reload

**Status**: Accepted  
**Date**: 2026-05-06

## Context

The daemon must add and remove DNS records dynamically as port-forwards appear and disappear (every 2 seconds). The DNS backend must support:

1. **Programmatic management** without elevated privileges at runtime
2. **Per-record granularity** — add/remove individual domains without affecting others
3. **Fast application** of changes (sub-second ideal; a few seconds acceptable)
4. **macOS and Linux compatibility** without two completely separate implementations
5. **Zero DNS leakage** — queries must never reach external resolvers

### Options evaluated

**`/etc/hosts`**: No wildcard support. Each change requires a write to a system-owned file (root). No partial reload — all processes re-read it on their next lookup, introducing a race. Editing it programmatically risks corrupting unrelated entries.

**Custom DNS server (Go, CoreDNS plugin)**: Embedded DNS server in the daemon binary. Complete control, no external dependency. However, requires the daemon to bind to port 53 or run a redirect (both need root), and conflicts with existing system DNS (systemd-resolved, mDNSResponder) on both platforms.

**dnsmasq**: Lightweight DNS/DHCP server available on both platforms. Supports:
- A `conf-dir` directive that watches a directory for `*.conf` files
- Per-file `address=/name/ip` directives (one file per domain)
- `SIGHUP` for live config reload without restart
- On macOS, Homebrew manages it as a user service (no root at runtime)
- On Linux, the system package integrates with systemd

### Per-domain drop-in files vs. a single managed file

A single file approach (rewrite the whole file on each change) requires a read-modify-write cycle under a lock, creates a window where the file is partially written, and makes debugging harder. Per-domain files are atomic: write a new file, delete an old one. Each file contains exactly one record:

```
# /etc/dnsmasq.d/local-auto-domain/port-8081.conf
address=/app-a-http.tunnel.test/127.0.1.1
```

### SIGHUP vs. restart

dnsmasq supports `SIGHUP` to reload its configuration without dropping established connections or restarting listeners. This is the correct mechanism for live updates. A full `systemctl restart` / `brew services restart` would cause a brief DNS outage and is unnecessary.

## Decision

Use dnsmasq as the DNS backend. Create one `.conf` file per active port-forward in a dedicated drop-in directory (`/etc/dnsmasq.d/local-auto-domain/` on Linux, `$HOMEBREW_PREFIX/etc/dnsmasq.d/local-auto-domain/` on macOS). Add/remove records by creating/deleting individual files, then signal dnsmasq with `pkill -HUP dnsmasq`.

The drop-in directory is created during `lad setup` with ownership transferred to the current user, so the daemon can write to it without sudo at runtime.

The main `dnsmasq.conf` gets a single appended line:
```
conf-dir=/path/to/local-auto-domain,*.conf
```

## Consequences

**Positive**
- Atomic per-record operations: no lock needed, no partial-write window.
- `SIGHUP` applies changes in milliseconds without a DNS outage.
- User-writable drop-in dir means the daemon runs without elevated privileges.
- Easy to inspect: `ls /etc/dnsmasq.d/local-auto-domain/` shows all active mappings.
- Both platforms share the same `Manager` implementation; only the path differs.

**Negative**
- dnsmasq must be installed and running as a prerequisite; `lad setup` handles this but it is an external dependency.
- `pkill -HUP dnsmasq` is a process-name signal — could affect an unrelated dnsmasq instance if one exists on the system. A PID-file approach would be safer but dnsmasq's PID file location varies by platform.
- On macOS, dnsmasq runs as a Homebrew service under the user account; on Linux it runs as a system service. The `pkill` approach works in both cases but signal delivery is not guaranteed if the process is in a zombie or stopped state.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| `/etc/hosts` | No wildcard; root required for every change; no atomic reload |
| Custom embedded DNS server | Requires binding port 53 (root); conflicts with system resolver |
| Single managed dnsmasq config file | Non-atomic writes; harder to debug; unnecessary complexity |
| Full dnsmasq restart on each change | Causes brief DNS outage; slow; unnecessary |
