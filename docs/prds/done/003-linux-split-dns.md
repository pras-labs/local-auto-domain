# PRD 003: Linux Split-DNS via lad-dns Dummy Interface

**Status**: Done  
**Date**: 2026-05-08

---

## Problem

On Linux, after `lad setup`, domains like `myapp-http.tunnel.test` resolved correctly when queried directly against dnsmasq (`dig @127.0.0.1 myapp-http.tunnel.test`) but returned NXDOMAIN through the system resolver (`getent hosts myapp-http.tunnel.test`). `curl` and browsers use the system resolver, so the feature was effectively broken on Linux.

Root cause: the initial implementation wrote `DNS=127.0.0.1` and `Domains=~tunnel.test` to a global systemd-resolved drop-in. This adds `127.0.0.1` to the global DNS server pool alongside DHCP-provided servers. systemd-resolved picks the "current" server in the global scope — `1.1.1.1` (from DHCP) wins. `1.1.1.1` returns NXDOMAIN for `tunnel.test`; resolved does not fall back to `127.0.0.1`.

There is no way to force a DNS server to "win" in the global scope without removing other servers from it.

---

## Goals

| Goal                                       | Metric                                                                                        |
| ------------------------------------------ | --------------------------------------------------------------------------------------------- |
| `tunnel.test` resolves via system resolver | `getent hosts myapp.tunnel.test` returns `127.0.1.X`                                          |
| Global DNS unaffected                      | `curl https://google.com` still works; DHCP-provided servers still used for non-`tunnel.test` |
| Persistent across reboots                  | No manual steps after setup                                                                   |
| No interactive prompts at runtime          | No sudo required during daemon operation                                                      |

---

## Investigation

Six approaches were attempted before reaching a working solution. Each failure revealed a different constraint in systemd-resolved's `link_relevant()` function. See [ADR-011](../../adr/011-linux-split-dns-lad-dns-dummy-interface.md) for the full investigation.

| Approach                                              | Failure reason                                                                                                       |
| ----------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Global resolved drop-in (`DNS=` in `resolved.conf.d`) | `1.1.1.1` wins in the global scope; resolved doesn't fall back                                                       |
| `resolvectl dns lo 127.0.0.1`                         | Requires `org.freedesktop.network1` D-Bus (systemd-networkd must be running)                                         |
| `.network` file for `lo` interface                    | `link_relevant()` returns false for `IFF_LOOPBACK` interfaces                                                        |
| Dummy interface without an address                    | `link_relevant()` returns false if address set is empty                                                              |
| Dummy interface + link-local address (`169.254.x.x`)  | `link_relevant()` skips link-local addresses                                                                         |
| Dummy interface + `192.0.2.1/32 Scope=host`           | `link_relevant()` skips `RT_SCOPE_HOST` addresses; networkd silently assigns host scope to `/32` on dummy interfaces |

---

## Solution

Create a `lad-dns` dummy network interface managed by systemd-networkd with a globally-scoped address, giving systemd-resolved an isolated per-link DNS scope for `tunnel.test`.

**`/etc/systemd/network/10-local-auto-domain-dns.netdev`**:

```ini
[NetDev]
Name=lad-dns
Kind=dummy
```

**`/etc/systemd/network/10-local-auto-domain-dns.network`**:

```ini
[Match]
Name=lad-dns

[Network]
DNS=127.0.0.1
Domains=~tunnel.test
LinkLocalAddressing=no
IPv6AcceptRA=no

[Address]
Address=192.0.2.1/32
Scope=global
```

`192.0.2.1` is from RFC 5737 TEST-NET-1 — permanently reserved for documentation, never publicly routed. `Scope=global` is mandatory: without it, networkd silently assigns `RT_SCOPE_HOST` to `/32` addresses on dummy interfaces and resolved still ignores the interface.

After `networkctl reload`, `resolvectl status lad-dns` shows `Current Scopes: DNS` — `tunnel.test` queries route exclusively to dnsmasq on `127.0.0.1`.

---

## dnsmasq Coexistence Fix

systemd-resolved's stub listener binds `127.0.0.53:53`. Without explicit binding directives, dnsmasq opens a wildcard socket on `0.0.0.0:53`, which can race with the stub.

`lad setup` writes to `/etc/dnsmasq.conf`:

```ini
listen-address=127.0.0.1
bind-interfaces
```

`listen-address` alone is insufficient — it opens a wildcard socket and filters by address. `bind-interfaces` forces an actual `bind()` call to `127.0.0.1:53` only.

`ensureConf()` uses line-by-line exact matching (`TrimSpace` comparison) to avoid false-matching commented-out defaults like `#bind-interfaces` that ship in the default dnsmasq config.

---

## Network Manager Detection

`configureSplitDNS()` detects the active network manager at setup time:

| Detected         | Action                                                                                               |
| ---------------- | ---------------------------------------------------------------------------------------------------- |
| systemd-networkd | Write `.netdev` + `.network`; `networkctl reload`                                                    |
| NetworkManager   | Write files; enable networkd if no conflicting `.network`/`.netdev` files exist; `networkctl reload` |
| dhcpcd           | Write files (inert); print dhcpcd.conf guidance                                                      |
| connman          | Write files (inert); print connman DNS proxy guidance                                                |
| ifupdown         | Write files (inert); print resolvconf / resolv.conf guidance                                         |
| none detected    | Write files (inert); print generic start-networkd message                                            |

### NetworkManager coexistence

networkd and NM can run simultaneously — networkd only manages interfaces with matching `.network` files, so enabling it for `lad-dns` alone leaves NM in full control of all physical interfaces.

Before enabling networkd, `networkdConflict()` checks for pre-existing `.network` or `.netdev` files in `/etc/systemd/network` and `/usr/lib/systemd/network`. If any are found (excluding our own files), setup prints a warning and does not enable networkd — the user must do so manually.

---

## Teardown

`teardownSplitDNS()`:

1. Removes `10-local-auto-domain-dns.netdev` and `10-local-auto-domain-dns.network`
2. Runs `networkctl reload`
3. If NetworkManager is also active (meaning networkd was enabled by setup), disables networkd again
4. Removes legacy artifacts from previous setup versions (oneshot service, resolved drop-in)

---

## Platforms That Cannot Use This Approach

`lad setup` configures split-DNS via a `lad-dns` dummy interface managed by systemd-networkd, with systemd-resolved providing per-domain query routing. Both must be active.

On distros without them (Alpine, minimal Debian/Ubuntu, some container images), the split-DNS step is skipped and `*.tunnel.test` queries won't reach dnsmasq. Manual alternatives:

- **Option A**: Make dnsmasq the system-wide resolver (`nameserver 127.0.0.1` in `/etc/resolv.conf`)
- **Option B**: Use per-domain forwarding via NetworkManager dnsmasq plugin or openresolv

See the README Linux section for detailed instructions.

---

## Data Storage

| Path                                                    | Content                                                                             |
| ------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `/etc/systemd/network/10-local-auto-domain-dns.netdev`  | lad-dns dummy interface definition                                                  |
| `/etc/systemd/network/10-local-auto-domain-dns.network` | lad-dns DNS routing config (DNS=127.0.0.1, ~tunnel.test, 192.0.2.1/32 Scope=global) |
| `/etc/dnsmasq.conf`                                     | Updated with `listen-address=127.0.0.1` and `bind-interfaces`                       |
| `/etc/sudoers.d/local-auto-domain`                      | `NOPASSWD: /usr/bin/systemctl reload dnsmasq`                                       |

---

## Implementation Files

| File                              | Change                                                                                                                                                           |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/dnsmasq/setup_linux.go` | `writeNetworkdDNSConfig()`, `configureSplitDNS()`, `configureSplitDNSNM()`, `networkdConflict()`, `teardownSplitDNS()`, `ensureConf()` line-by-line matching fix |

---

## Decision Records

| ADR                                                             | Decision                                                                                                                        |
| --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| [011](../../adr/011-linux-split-dns-lad-dns-dummy-interface.md) | Six-attempt investigation; final fix: lad-dns dummy with `192.0.2.1/32 Scope=global`; network manager detection; NM coexistence |
