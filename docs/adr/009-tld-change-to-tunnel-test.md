# ADR-009: Change TLD from `tunnel.localhost` to `tunnel.test`

**Status**: Accepted  
**Date**: 2026-05-07  
**Supersedes**: [ADR-001](001-use-tunnel-localhost-tld.md)

## Context

ADR-001 chose `tunnel.localhost` as the TLD, reasoning that macOS's `/etc/resolver/tunnel.localhost` mechanism would route `*.tunnel.localhost` queries to dnsmasq, and that RFC 6761 compliance would prevent upstream DNS leakage.

This assumption was incorrect. Testing on macOS 15 (Sequoia) revealed that `*.tunnel.localhost` never reaches dnsmasq:

```
$ dscacheutil -q host -a name anything-random.tunnel.localhost
name: localhost
ip_address: 127.0.0.1
```

The same result occurs for every `*.localhost` name, regardless of whether a resolver file exists and regardless of dnsmasq configuration. Querying dnsmasq directly works correctly:

```
$ dig @127.0.0.1 argocd-server-https.tunnel.localhost +short
127.0.1.1   # correct — dnsmasq has the record
```

But the system resolver returns `127.0.0.1` (treating the name as bare `localhost`):

```
$ dscacheutil -q host -a name argocd-server-https.tunnel.localhost
name: localhost
ip_address: 127.0.0.1
```

### Root cause

macOS implements RFC 6761 §6.3, which states that `localhost` and its subdomains MUST resolve to the loopback address and MUST NOT be forwarded to DNS servers. mDNSResponder enforces this before consulting `/etc/resolver/` files. The result:

- Any `*.localhost` query — regardless of labels — resolves immediately to `127.0.0.1`/`::1`
- `/etc/resolver/tunnel.localhost` is parsed by `scutil --dns` (the file is syntactically valid) but is never acted upon for actual lookups
- The practical consequence: curl connects to `127.0.0.1:8443` (the raw port-forward), not `127.0.0.1:8443` (the TLS proxy), so TLS termination is bypassed and clients see the remote service's cert, triggering "unable to get local issuer certificate"

ADR-001 incorrectly assessed `.test` as lacking a standard macOS resolver hook. In practice, `/etc/resolver/test` is respected normally by mDNSResponder — it has no special-case treatment for `.test`.

### Why `.test` is correct

- RFC 2606 reserves `.test` for testing and documentation. It will never be delegated in the global DNS.
- macOS mDNSResponder does NOT special-case `.test`; `/etc/resolver/test` routes `*.test` queries to the specified nameserver as expected.
- `.test` is not in any HSTS preload list, so browsers handle HTTP on `*.tunnel.test` without forcing HTTPS.
- Linux systemd-resolved accepts `Domains=~tunnel.test` with no special treatment.
- No upstream DNS leakage: RFC 2606 reserved names are dropped by compliant resolvers.

## Decision

Change the default TLD from `tunnel.localhost` to `tunnel.test`.

| Changed item | Old | New |
|---|---|---|
| Default TLD constant (`config.go`) | `tunnel.localhost` | `tunnel.test` |
| macOS resolver file | `/etc/resolver/tunnel.localhost` | `/etc/resolver/test` |
| Linux systemd-resolved domain | `Domains=~tunnel.localhost` | `Domains=~tunnel.test` |
| Wildcard cert SANs (`tlscert.go`) | `*.tunnel.localhost, tunnel.localhost` | `*.tunnel.test, tunnel.test` |
| Domain pattern | `{id}-{svc}.tunnel.localhost` | `{id}-{svc}.tunnel.test` |

`Teardown()` removes both `/etc/resolver/test` and `/etc/resolver/tunnel.localhost` to clean up users who had the old setup.

`LoadCert` detects old certs (SANs don't include `*.tunnel.test`) and forces regeneration via `EnsureCert`.

Users who had `tunnel.localhost` configured must run `lad uninstall && lad setup` to apply the new resolver file and regenerated cert.

## Consequences

**Positive**
- DNS resolution actually works on macOS: `*.tunnel.test` queries reach dnsmasq and return the correct `127.0.1.X` address.
- TLS proxy is now in the traffic path for HTTPS forwards; `*.tunnel.test` cert is presented and trusted.
- curl works without cert errors after `lad setup`.
- No change to the privilege model or external dependencies.

**Negative**
- Breaking change: existing `lad setup` installations must run `lad uninstall && lad setup`.
- The TLD `tunnel.test` is slightly less intuitive than `tunnel.localhost` as a hint of "local-only", but `.test` is well-understood by developers.
- Technically, `.test` could be queried by other software on the system as if it were a real TLD (dnsmasq won't intercept unless configured). In practice, all modern resolvers comply with RFC 2606 and drop `.test` queries.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| Keep `tunnel.localhost` | Confirmed broken on macOS — resolver file ignored, all `*.localhost` hardcoded to 127.0.0.1 |
| `/etc/hosts` per entry | No wildcard; root required for every add/remove; doesn't scale with dynamic forwards |
| Custom single-label TLD (`.lad`) | Not reserved; may leak to upstream resolvers; resolver file would be `/etc/resolver/lad` which is non-obvious |
| `.tunnel.internal` | Not reserved; may leak to corporate DNS resolvers |
| Embedded DNS server on port 53 | Requires root; conflicts with system resolver |

## References

- RFC 6761 §6.3 — Special handling of `localhost` and subdomains
- RFC 2606 — Reserved TLDs: `.test`, `.example`, `.invalid`, `.localhost`
- ADR-001 — Original TLD decision (superseded)
