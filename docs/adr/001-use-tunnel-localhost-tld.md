# ADR-001: Use `.tunnel.localhost` as the default TLD

**Status**: Superseded by [ADR-009](009-tld-change-to-tunnel-test.md)
**Date**: 2026-05-06

## Context

`local-auto-domain` generates resolvable domain names for `ssh -L` and `kubectl port-forward` connections. Each forwarded port must be addressable via a stable, human-readable hostname that:

1. Resolves only on the local machine
2. Works without modifying `/etc/hosts`
3. Does not conflict with real DNS namespaces
4. Routes through dnsmasq for programmatic management

The obvious candidate was a `.local` TLD (e.g., `myapp-http.local`), which is already familiar to developers from tools like Avahi and mDNS-SD.

### The `.local` problem on macOS

RFC 6762 reserves `.local` for Multicast DNS (mDNS). macOS enforces this at the OS resolver level: any query for `*.local` is intercepted by the Bonjour/mDNS stack (`mDNSResponder`) before reaching dnsmasq or any other DNS resolver.

Consequences:
- Queries for `.local` names never reach dnsmasq, so custom records are silently ignored.
- `/etc/resolver/local` is ineffective — macOS still routes `.local` through mDNS first.
- Behavior is not configurable without disabling Bonjour entirely, which breaks AirDrop, AirPlay, and local service discovery.
- The issue is macOS-specific but affects the primary target platform.

### Why not a custom TLD (e.g., `.tunnel`, `.dev`, `.test`)?

- `.dev` and `.app` are real gTLDs owned by Google; browsers force HTTPS via HSTS preload lists, breaking plain HTTP workflows.
- `.test` is IANA-reserved (RFC 2606) and safe, but requires system-level DNS configuration that varies across platforms and has no standard resolver hook on macOS without a full DNS override.
- Arbitrary TLDs (`.tunnel`, `.internal`) require writing a resolver stub and are not guaranteed to be intercepted locally — they may leak to upstream resolvers depending on split-horizon DNS configuration.

### `.localhost` subdomain behavior

RFC 6761 §6.3 specifies that `localhost` and any subdomain of `localhost` MUST resolve to a loopback address and MUST NOT be sent to DNS servers. Modern OS resolvers treat `*.localhost` as inherently local:

- **macOS**: The `/etc/resolver/` mechanism accepts a file named `tunnel.localhost`, directing all `*.tunnel.localhost` queries to a local nameserver (dnsmasq) without mDNS interference.
- **Linux (systemd-resolved)**: Split-DNS configuration routes `tunnel.localhost` to dnsmasq via a per-link DNS domain.
- **No upstream leakage**: Resolvers that comply with RFC 6761 drop `*.localhost` queries before forwarding to external DNS.

## Decision

Use `tunnel.localhost` as the default TLD for all generated domain names.

Generated domains follow the pattern:

```
{identifier}-{service}.tunnel.localhost
```

Example: `10-0-0-2-http.tunnel.localhost`, `argocd-server-https.tunnel.localhost`

The resolver stub file is written to `/etc/resolver/tunnel.localhost` (macOS) or configured via systemd-resolved (Linux) during `lad setup`.

The TLD is user-overridable via `tld:` in `~/.config/local-auto-domain/config.yaml` for environments that require a different namespace.

## Consequences

**Positive**
- Zero conflict with Bonjour/mDNS on macOS; `.tunnel.localhost` queries never reach `mDNSResponder`.
- RFC 6761 guarantees no upstream DNS leakage regardless of network configuration.
- `/etc/resolver/tunnel.localhost` is the standard macOS mechanism for split-horizon DNS; no workarounds needed.
- Compatible with all browsers and HTTP clients without HSTS or certificate preload complications.

**Negative**
- Multi-label subdomains of `localhost` (e.g., `foo.tunnel.localhost`) are not universally supported in all RFC 6761 implementations. Some older or non-compliant resolvers may not recognize them as localhost-scoped. The dnsmasq-backed setup mitigates this in practice.
- The `tunnel` sub-label is load-bearing: using `*.localhost` directly (without a sub-label) would conflict with the bare `localhost` resolver and is not suitable as a wildcard namespace.

## Alternatives Rejected

| TLD | Reason rejected |
|-----|----------------|
| `.local` | Intercepted by mDNS on macOS; dnsmasq records silently ignored |
| `.dev` / `.app` | Real gTLDs with HSTS preload; browsers force HTTPS |
| `.test` | Safe per RFC 2606, but no standard macOS resolver hook; potential upstream leakage |
| `.internal` | No IANA reservation; may leak to corporate DNS resolvers |
| `/etc/hosts` entries | No wildcard support; requires root write on every change |

## References

- RFC 6761 — Special-Use Domain Names (`localhost`)
- RFC 6762 — Multicast DNS (`.local` reservation)
- RFC 2606 — Reserved Top Level DNS Names (`.test`, `.example`, `.invalid`, `.localhost`)
- macOS `resolver(5)` man page — `/etc/resolver/` directory semantics
