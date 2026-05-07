# ADR-010: dnsmasq Dynamic Reload via addn-hosts

**Status**: Accepted  
**Date**: 2026-05-07

## Context

The original implementation wrote per-domain dnsmasq configuration as individual files in a `conf-dir` drop-in directory:

```
/etc/dnsmasq.d/local-auto-domain/port-8080.conf
  → address=/myapp-http.tunnel.test/127.0.1.1
```

The `Manager.reload()` then sent `SIGHUP` to dnsmasq.

### Discovery: conf-dir is startup-only

Testing revealed that dnsmasq does **not** re-read `conf-dir` files on SIGHUP. From the dnsmasq man page:

> On receiving SIGHUP dnsmasq clears its cache and then re-reads `/etc/hosts` and `/etc/ethers` and any file given by `--addn-hosts` option.

`conf-dir` entries — including `address=` directives — are read exclusively at startup. Sending SIGHUP after writing a new conf file has no effect. Domains written while dnsmasq is running were silently never resolvable without restarting dnsmasq.

Confirmed: `dig @127.0.0.1 -p 5300 <new-domain>` returned NXDOMAIN immediately after `pkill -HUP dnsmasq`, even though the conf file existed on disk.

## Decision

Replace per-port `address=` conf files with a single `hosts` file registered via `addn-hosts`.

### Mechanism

During `lad setup`, add to `dnsmasq.conf`:

```
addn-hosts=/path/to/local-auto-domain/hosts
```

The `Manager` maintains this single file in `/etc/hosts` format:

```
127.0.1.1   myapp-http.tunnel.test
127.0.1.2   argocd-server-https.tunnel.test
```

On every domain add or remove:
1. Rewrite the hosts file from all active per-port state files
2. Send SIGHUP — dnsmasq re-reads the addn-hosts file immediately

Per-port `port-N.conf` state files are retained in the drop-in dir but serve only as persistent state for the daemon's recovery after a restart. dnsmasq does not read them at runtime for dynamic updates.

### macOS: non-privileged port + user LaunchAgent

**Problem**: dnsmasq started as a root LaunchDaemon drops to `nobody`. A user-space `pkill -HUP dnsmasq` fails because a non-root user cannot signal a process owned by a different user.

**Fix**: Configure dnsmasq to listen on port 5300 (non-privileged) via a `00-port.conf` entry in the drop-in dir. Update `/etc/resolver/test` to include `port 5300`. Start dnsmasq as a user-level LaunchAgent — it no longer needs root to bind the port.

`pkill -HUP dnsmasq` from the user-space daemon works without sudo.

#### launchctl asuser for setup

`lad setup` is invoked with sudo. Running `sudo -u <user> brew services start dnsmasq` from a root context fails with `launchctl bootstrap gui/<uid>` exit code 5 — the user's GUI session token is not available in a `sudo -u` context.

Fix: use `launchctl asuser <uid> brew services start dnsmasq`. The `asuser` subcommand runs the command inside the user's actual session, carrying the required session token.

### Linux: NOPASSWD sudoers rule

dnsmasq on Linux must bind port 53, so it requires root and runs as a system service. It drops to `nobody` after start. `pkill -HUP dnsmasq` and direct `kill` from a user process both fail.

`systemctl reload dnsmasq` is the correct approach: systemd tracks the PID even after privilege drop and sends SIGHUP to the correct process.

During `lad setup`, write `/etc/sudoers.d/local-auto-domain`:

```
<username> ALL=(ALL) NOPASSWD: /usr/bin/systemctl reload dnsmasq
```

`manager_linux.go` uses `sudo systemctl reload dnsmasq` — no interactive prompt, targeted to exactly one command.

## Consequences

**Positive**
- Domain updates are live within 2 seconds via SIGHUP — no dnsmasq restart required
- No runtime sudo on macOS — dnsmasq runs as the current user
- Linux sudoers rule is scoped to one specific command — minimal privilege
- Daemon crash/restart recovery: per-port state files allow hosts file reconstruction on next poll

**Negative**
- `addn-hosts` must be registered at dnsmasq startup; adding it to `dnsmasq.conf` after dnsmasq is already running requires a one-time restart (after `lad setup` on existing installations)
- macOS port 5300 is an implementation detail that must be consistent across `00-port.conf`, `/etc/resolver/test`, and `writeResolver()` — a mismatch silently breaks resolution
- Linux sudoers file is username-specific; reinstalling as a different user requires re-running `lad setup`

## Verification

- **macOS**: `dns-sd -q <domain> A` — correct tool; uses mDNSResponder (same path as curl). `dscacheutil` and `host`/`dig` are unreliable for this.
- **Linux**: `getent hosts <domain>` — uses the full resolver stack including systemd-resolved per-domain routing.
