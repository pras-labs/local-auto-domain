# ADR-006: macOS Loopback Alias Strategy — Bulk ifconfig at Setup + LaunchDaemon

**Status**: Accepted  
**Date**: 2026-05-06

## Context

ADR-002 requires unique loopback IPs from `127.0.1.0/24` for each active port-forward. On Linux, the entire `127.0.0.0/8` block routes to the loopback interface by default — any `127.x.x.x` address is immediately bindable without configuration.

On macOS, only `127.0.0.1` is aliased to `lo0` by default. Attempting to bind to `127.0.1.1` without first creating the alias results in `bind: can't assign requested address`. The `lo0` interface does not respond to the full `/8` block the way Linux's `lo` does.

### Options for creating aliases

**Option A — Create alias at bind time (runtime sudo)**  
The daemon calls `sudo ifconfig lo0 alias 127.0.1.X` each time a new port-forward is detected.

Problem: Requires the daemon to invoke `sudo` at runtime. This means the daemon must be run with `sudo`, or `sudoers` must be configured to allow `ifconfig` without a password prompt. Both are unacceptable for a developer tool that should run as a user service.

**Option B — Create all aliases at setup time (one-time sudo)**  
`lad setup` (which already requires sudo for dnsmasq and `/etc/resolver` configuration) pre-creates aliases for the full pool range `127.0.1.1–127.0.1.100` in one batch.

The daemon then calls `EnsureAlias` as a safety net — if the alias exists, `ifconfig` returns immediately without error. If the machine has been rebooted and the LaunchDaemon hasn't run yet, the safety net call will fail (no sudo), but the daemon logs a warning and continues — the proxy bind will fail gracefully for that specific forward.

**Boot persistence**: `ifconfig` aliases are not persistent across reboots. They must be recreated.

**Option C — LaunchDaemon for boot persistence**  
A root-owned LaunchDaemon plist at `/Library/LaunchDaemons/com.pras-labs.local-auto-domain-loopback.plist` runs a shell loop on every boot, recreating all aliases before any user session starts. This runs as root (`RunAtLoad: true`) and requires no user interaction.

Alternatives for persistence:
- `/etc/rc.local`: deprecated on macOS, not reliable.
- LaunchAgent (user session): runs too late — after network services start but potentially after the daemon is already running and trying to bind.
- `networksetup` / `scutil`: no support for loopback alias persistence.

## Decision

`lad setup` performs three steps for loopback aliases:
1. Creates `127.0.1.1–127.0.1.100` immediately via `sudo ifconfig lo0 alias` (100 aliases in a loop).
2. Installs `/Library/LaunchDaemons/com.pras-labs.local-auto-domain-loopback.plist` (via `sudo launchctl load -w`) containing a shell loop that recreates all 100 aliases at boot.
3. Loads the LaunchDaemon immediately so aliases survive the current session.

The daemon's `netutil.EnsureAlias(ip)` calls `ifconfig lo0 alias <ip>` as a safety net with error ignored. `netutil.RemoveAlias(ip)` calls `ifconfig lo0 -alias <ip>` when a forward exits (cleans up the specific alias, though it is immediately recreated by the boot daemon if the machine is rebooted).

On Linux, `loopback_linux.go` implements both functions as no-ops.

The pool size (100) covers the expected maximum number of simultaneous port-forwards on a developer workstation. It is hardcoded to keep setup predictable; users with larger needs can run `sudo ifconfig lo0 alias 127.0.1.X` manually for additional addresses.

`lad uninstall` reverses this: unloads and removes the LaunchDaemon, then removes all 100 aliases.

## Consequences

**Positive**
- Daemon runs entirely as a user process after setup; no runtime sudo required.
- Boot persistence is handled by a root LaunchDaemon — standard macOS mechanism.
- Pre-creating all aliases makes bind always succeed without any alias check.
- Setup is idempotent: re-running `lad setup` with aliases already present is harmless.

**Negative**
- Requires a one-time sudo setup step; cannot be zero-configuration.
- 100 aliases are pre-created regardless of how many forwards will be active. Minor aesthetic issue — no meaningful resource cost.
- Pool is statically sized at 100; exceeding it requires manual intervention.
- The LaunchDaemon runs as root and executes a shell script — a privileged operation that users should be aware of.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| Runtime sudo per alias | Daemon would require sudo or sudoers exceptions; unacceptable for a user-mode daemon |
| Single alias at startup (no pool) | Cannot pre-allocate a pool; defeats the allocation strategy |
| `/etc/rc.local` persistence | Deprecated on macOS; unreliable |
| LaunchAgent (user session) | Runs after login, potentially after daemon first poll; race condition |
| Require user to pre-create aliases | Too much manual setup; defeats usability goal |
