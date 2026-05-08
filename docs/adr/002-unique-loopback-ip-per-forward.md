# ADR-002: Allocate a Unique Loopback IP per Port-Forward

**Status**: Accepted  
**Date**: 2026-05-06

## Context

Each port-forward the daemon tracks needs to be independently addressable by a stable domain name. The daemon starts a TCP proxy that clients connect to on a well-known service port (e.g., `:8080` for HTTP, `:5432` for PostgreSQL). The problem is that a single machine frequently has two or more simultaneous forwards targeting the same service port on different remote hosts — for example:

```
kubectl port-forward svc/app-a 8081:80
kubectl port-forward svc/app-b 8082:80
```

Both need to be reachable as `app-a-http.tunnel.test` and `app-b-http.tunnel.test`, both on port `:8080`.

### The port-collision problem

If all proxies share the same IP (`127.0.0.1`), only one process can listen on `127.0.0.1:8080` at a time. Routing multiple domains to the same IP:port is only possible with application-layer multiplexing (HTTP Host header, TLS SNI), which excludes non-HTTP protocols like PostgreSQL, Redis, and MySQL.

### Alternatives considered

**Option A — Random/assigned proxy ports per forward**  
Each forward gets a unique local port (e.g., `:10001`, `:10002`). The domain resolves to `127.0.0.1` and the client must use the non-standard port.

Problem: breaks the ergonomics goal. Users must know and use `psql -h mydb.tunnel.test -p 10004` instead of the standard `psql -h mydb.tunnel.test`. Defeats the purpose of using a domain name.

**Option B — External reverse proxy (nginx, HAProxy, Envoy)**  
Install and configure a reverse proxy that routes by domain name or SNI.

Problem: HTTP-only or TLS-SNI-only — cannot handle arbitrary TCP protocols like raw PostgreSQL. Adds a heavyweight external dependency. Configuration management becomes complex (one entry per dynamic forward).

**Option C — Unique loopback IP per forward**  
Allocate an address from the `127.0.1.0/24` range for each active port-forward. Each proxy binds to its own IP, so multiple proxies can all listen on `:8080` without collision.

## Decision

Allocate a unique IP from `127.0.1.1–127.0.1.254` for each active port-forward. dnsmasq resolves the domain to that specific IP. The proxy binds to `{uniqueIP}:{servicePort}` and forwards to `127.0.0.1:{localPort}`.

```
app-a-http.tunnel.test → 127.0.1.1 → proxy :8080 → 127.0.0.1:8081
app-b-http.tunnel.test → 127.0.1.2 → proxy :8080 → 127.0.0.1:8082
```

An `ipalloc.Allocator` maintains the pool of available addresses in a thread-safe bitmap. Freed IPs return to the pool and are reused.

**Linux**: `127.0.0.0/8` routes to loopback out of the box; any `127.0.1.X` address is immediately bindable without configuration.  
**macOS**: Only `127.0.0.1` is aliased by default; additional addresses must be added via `ifconfig lo0 alias` (see ADR-007).

## Consequences

**Positive**
- Multiple forwards to the same service port are independently addressable with no port-number disambiguation needed by the user.
- Works for any TCP protocol, not just HTTP.
- No external proxy dependency.
- IP allocation and deallocation are O(1) and fully reversible when a forward exits.

**Negative**
- Pool is limited to 254 simultaneous forwards. Sufficient for developer workstation use but not for high-scale scenarios.
- macOS requires pre-created loopback aliases (see ADR-007), adding setup complexity.
- Unique IPs per forward mean dnsmasq config entries must use per-entry `address=` directives rather than a shared wildcard IP, producing more config files.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| Unique proxy port per forward | Non-standard ports break protocol clients; ergonomics goal violated |
| Shared IP + HTTP Host routing | Only works for HTTP; PostgreSQL, Redis, MySQL excluded |
| External reverse proxy | HTTP/SNI-only; heavyweight dependency; dynamic config complexity |
