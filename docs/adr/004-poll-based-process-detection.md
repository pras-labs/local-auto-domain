# ADR-004: Poll-Based Port-Forward Detection via lsof/ss

**Status**: Accepted  
**Date**: 2026-05-06

## Context

The daemon must detect when an `ssh -L` or `kubectl port-forward` process starts or exits, then act within a few seconds. Detection must work on both macOS and Linux without requiring kernel modules, eBPF, or elevated privileges.

### What needs to be detected

For each active port-forward:
- The **local port** being listened on (to know what to proxy)
- The **tool** (`ssh` or `kubectl`) and its **command line** (to derive the domain name and remote info)
- The **PID** (to track lifecycle)

### Detection approaches considered

**inotify / FSEvents (file system events)**  
Watching `/proc/net/tcp` (Linux) or using FSEvents (macOS) for socket-related filesystem changes. Complex, platform-specific, and does not provide cmdline access. `inotify` on procfs is unreliable — changes to `/proc/net/tcp` are not always inotify-visible.

**netlink (Linux) / System Configuration framework (macOS)**  
Netlink `SOCK_DIAG` can receive socket events on Linux. macOS has no equivalent public API for TCP socket lifecycle events. Requires raw socket access (CAP_NET_ADMIN or root) on many Linux configurations.

**Tracing (ptrace, eBPF, DTrace)**  
Extremely powerful but requires root or specific capabilities on Linux; DTrace on macOS requires SIP-exempt entitlements. Introduces significant complexity and is inappropriate for a developer workstation tool.

**Polling via `lsof` / `ss` + `/proc`**  
- **macOS**: `lsof -nP -iTCP -sTCP:LISTEN` lists all TCP listening sockets with PID, command, and address. Available by default, no special permissions.
- **Linux**: `ss -tlnp` lists TCP listening sockets. `/proc/{pid}/cmdline` provides the full command line. No special permissions required for user-owned processes.

Both tools produce stable, parseable output and are universally available on their respective platforms.

### Poll interval

A 2-second poll interval was chosen as the default. Port-forward detection latency (time from command run to domain appearing) is at most 2 seconds plus dnsmasq reload time. This is imperceptible in practice — the domain is available before the user would normally switch windows to use it.

CPU overhead is negligible: `lsof`/`ss` are fast operations taking <50ms on a loaded system.

## Decision

Poll for active port-forwards every `poll_interval` (default: 2 seconds) using platform-specific scanners:

- **macOS** (`scanner_darwin.go`): parse `lsof -nP -iTCP -sTCP:LISTEN` output, cross-reference PIDs with process cmdlines
- **Linux** (`scanner_linux.go`): parse `ss -tlnp` output for port and PID, read `/proc/{pid}/cmdline` for command details

Both scanners implement the `Scanner` interface, returning a `[]PortForward` slice. The daemon reconciles the current slice against its active state map: new entries are added, disappeared entries are removed.

The poll interval is user-configurable via `poll_interval:` in config.

## Consequences

**Positive**
- No elevated privileges required at runtime.
- Works on both platforms with the same daemon loop logic.
- Simple to reason about: the daemon's state is always derived from a fresh scan, not from accumulated events. No event delivery failures to handle.
- The poll model naturally handles edge cases: if the daemon misses a process exit (e.g., due to a crash), the next poll corrects the state.

**Negative**
- Up to `poll_interval` detection latency. Not suitable if sub-second detection is required (not a goal here).
- Spawning `lsof`/`ss` subprocesses every 2 seconds adds minor overhead. On a system with many open files, `lsof` can take 50–200ms.
- Process cmdline parsing is fragile: unusual quoting, truncated cmdlines (`/proc` truncates at 4096 bytes by default), or wrapper scripts can confuse the parser.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| inotify / FSEvents on procfs | Unreliable for socket events; platform-specific |
| Netlink SOCK_DIAG | Requires capabilities; no macOS equivalent |
| eBPF / DTrace / ptrace | Requires root or entitlements; disproportionate complexity |
| systemd socket activation | Only covers systemd-managed processes; not general |
