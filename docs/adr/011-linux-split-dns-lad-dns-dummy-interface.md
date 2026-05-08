# ADR-011: Linux Split-DNS via lad-dns Dummy Interface

**Status**: Accepted  
**Date**: 2026-05-08

## Context

`lad setup` needs to route `*.tunnel.test` DNS queries to dnsmasq on `127.0.0.1` while leaving all other DNS unaffected. The implementation went through four failed approaches before reaching the current solution.

## Attempts and failures

### Attempt 1: Global systemd-resolved drop-in

```
# /etc/systemd/resolved.conf.d/local-auto-domain.conf
[Resolve]
DNS=127.0.0.1
Domains=~tunnel.test
```

**Failure**: `DNS=` in a global resolved drop-in adds `127.0.0.1` to the global DNS server pool alongside DHCP-provided servers (e.g. `1.1.1.1`). systemd-resolved picks the "current" server for the global scope — `1.1.1.1` wins. NXDOMAIN from `1.1.1.1` is a valid response; resolved does not fall back to `127.0.0.1`.

### Attempt 2: Per-link DNS on lo via resolvectl

```bash
resolvectl dns lo 127.0.0.1
resolvectl domain lo ~tunnel.test
```

**Failure**: `resolvectl dns <interface>` goes through `org.freedesktop.network1` (systemd-networkd's D-Bus service). On systems where networkd is not running, this fails immediately:

```
Failed to set DNS configuration: Unit dbus-org.freedesktop.network1.service not found.
```

Even on systems where networkd is active, `resolvectl` changes are ephemeral and do not survive reboots.

### Attempt 3: systemd-networkd .network file for lo

```
# /etc/systemd/network/10-local-auto-domain-lo.network
[Match]
Name=lo

[Network]
DNS=127.0.0.1
Domains=~tunnel.test
```

**Failure**: `resolvectl status lo` showed:

```
Link 1 (lo)
Current Scopes: none
     DNS Servers: 127.0.0.1
      DNS Domain: ~tunnel.test
```

systemd-resolved's `link_relevant()` checks `IFF_LOOPBACK` and returns false for the loopback interface, regardless of configured DNS servers. The DNS scope is never activated.

### Attempt 4: Dummy interface without an address

```
[NetDev]
Name=lad-dns
Kind=dummy

[Network]
DNS=127.0.0.1
Domains=~tunnel.test
LinkLocalAddressing=no
```

**Failure**: `link_relevant()` also returns false when a link has no addresses:

```c
if (set_isempty(l->addresses))
    return false;
```

Still `Current Scopes: none`.

### Attempt 5: Dummy interface with link-local address (LinkLocalAddressing=ipv4)

Adding `LinkLocalAddressing=ipv4` assigns a `169.254.x.x` address. Still `Current Scopes: none`.

`link_relevant()` explicitly skips link-local addresses:

```c
if (in_addr_is_link_local(a->family, &a->in_addr) > 0)
    continue;
```

### Attempt 6: Dummy interface with 192.0.2.1/32 Scope=host

Adding `Address=192.0.2.1/32` with `Scope=host` in the `[Address]` section still produces `Current Scopes: none`.

`link_relevant()` skips `RT_SCOPE_HOST` addresses before the address loop:

```c
if (a->scope == RT_SCOPE_HOST)
    continue;
```

`ip addr show` confirmed `scope host` even though `Scope=host` was not in the config file — networkd silently assigns `RT_SCOPE_HOST` to `/32` addresses on dummy interfaces unless `Scope=global` is stated explicitly.

## Decision

Create a `lad-dns` dummy interface managed by systemd-networkd with a globally-scoped non-link-local address:

```
# /etc/systemd/network/10-local-auto-domain-dns.netdev
[NetDev]
Name=lad-dns
Kind=dummy

# /etc/systemd/network/10-local-auto-domain-dns.network
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

`192.0.2.1` is from RFC 5737 TEST-NET-1 — permanently reserved for documentation, never publicly routed. `Scope=global` is required explicitly; without it, networkd assigns `RT_SCOPE_HOST` and resolved still ignores the address.

After `networkctl reload`, `resolvectl status lad-dns` shows:

```
Link 30 (lad-dns)
Current Scopes: DNS
     DNS Servers: 127.0.0.1
      DNS Domain: ~tunnel.test
```

`tunnel.test` queries are now routed exclusively to dnsmasq on `127.0.0.1`.

## Network manager detection

`configureSplitDNS()` detects the active network manager at setup time:

| Active service      | Action                                                          |
| ------------------- | --------------------------------------------------------------- |
| systemd-networkd    | Write `.netdev` + `.network`; `networkctl reload`               |
| NetworkManager      | Write `.netdev` + `.network`; enable + start networkd if no conflicting `.network`/`.netdev` files exist in `/etc/systemd/network` or `/usr/lib/systemd/network`; `networkctl reload` |
| dhcpcd              | Write files (inert); print dhcpcd.conf guidance                 |
| connman             | Write files (inert); print connman DNS proxy guidance           |
| ifupdown            | Write files (inert); print resolvconf / resolv.conf guidance    |
| none detected       | Write files (inert); print generic start-networkd message       |

### NetworkManager coexistence

networkd and NM can run simultaneously. networkd only manages interfaces with matching `.network` files — so enabling networkd solely for `lad-dns` leaves NM in control of all physical interfaces. Before enabling, `networkdConflict()` checks for pre-existing `.network`/`.netdev` files that could match physical interfaces; if any are found, setup prints a warning and does not enable networkd.

## dnsmasq coexistence with systemd-resolved stub

dnsmasq must bind port 53. systemd-resolved's stub listener binds `127.0.0.53:53`. These do not conflict (different IPs). However, without explicit binding directives, dnsmasq opens a wildcard socket on `0.0.0.0:53` and filters by address — this can still race with the stub.

`lad setup` writes to `/etc/dnsmasq.conf`:

```
listen-address=127.0.0.1
bind-interfaces
```

`listen-address` alone is insufficient; `bind-interfaces` forces an actual `bind()` call to `127.0.0.1:53` only.

`ensureConf()` checks for exact line matches (line-by-line `TrimSpace` comparison) to avoid false matches against commented-out defaults such as `#bind-interfaces`.

## Teardown

`teardownSplitDNS()` removes both `.netdev` and `.network` files and runs `networkctl reload`. If NM is also active (meaning networkd was enabled during setup), it disables networkd again. Legacy artifacts from previous setup versions (oneshot service, resolved drop-in) are also removed.

## Consequences

**Positive**
- Isolated per-link DNS scope — `1.1.1.1` from DHCP never competes with `127.0.0.1` for `tunnel.test` queries
- Persistent across reboots: networkd reads `.netdev`/`.network` at startup
- No ephemeral `resolvectl` calls; no oneshot service needed
- `192.0.2.1` is TEST-NET — no routing side effects beyond a single harmless kernel host route

**Negative**
- Requires systemd-networkd (active or enabled). On pure NM systems without conflicting networkd files, setup enables networkd automatically. On systems with pre-existing networkd configs, a warning is printed and the user must enable networkd manually.
- The `Scope=global` requirement for `/32` on dummy interfaces is a non-obvious networkd behaviour. Without it, `Current Scopes: none` with no error message — silent failure.
- `lad-dns` interface appears in `ip link` and `networkctl` output — visible but harmless.
