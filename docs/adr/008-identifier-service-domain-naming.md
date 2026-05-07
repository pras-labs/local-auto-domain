# ADR-008: Identifier–Service Domain Naming Pattern

**Status**: Accepted  
**Date**: 2026-05-06

## Context

Each port-forward must be assigned a stable, human-readable domain name that:
1. Uniquely identifies the forward within the active set
2. Hints at what service is being forwarded
3. Works as a valid DNS label (lowercase, no special chars, ≤63 chars per label)
4. Does not require user configuration for the common case

The two most common use cases are:
- `ssh -L localPort:remoteHost:remotePort` — remote host IP is known (`10.0.0.4`)
- `kubectl port-forward svc/name localPort:remotePort` — Kubernetes resource name is known (`argocd-server`)

### What information is available per forward

| Field | SSH | kubectl |
|-------|-----|---------|
| Local port | ✓ | ✓ |
| Remote host (IP or hostname) | ✓ | — |
| Remote port | ✓ | ✓ |
| Resource name (svc/pod/deploy) | — | ✓ |
| Namespace | — | ✓ (optional) |

### Naming options considered

**`port-{localPort}.tunnel.test`**  
Simple but opaque. `port-8181.tunnel.test` gives no hint about what service it is or where it goes. Multiple forwards on the same host are indistinguishable.

**`{remoteHost}.tunnel.test`**  
For SSH, `10-0-0-4.tunnel.test` is informative but loses service-type context. Two SSH forwards to the same host on different ports produce the same name (collision).

**`{resource}-{namespace}.tunnel.test`**  
Includes namespace for kubectl. However, namespace is optional in the command and adds length without consistent value. Most developers know the service name; the namespace is secondary.

**`{identifier}-{service}.tunnel.test`**  
Combines a source identifier with the service type derived from the remote port. Provides both routing context (where it goes) and protocol context (what it is).

## Decision

Domain pattern: `{identifier}-{service}.tunnel.test`

**Identifier derivation** (priority order):
1. Manual override: `cfg.Overrides[localPort]` — user-set via `lad set <port> <name>`
2. SSH: remote host with dots replaced by dashes (`10.0.0.4` → `10-0-0-4`)
3. kubectl: resource name stripped of type prefix (`svc/argocd-server` → `argocd-server`)
4. Fallback: `port-{localPort}` (when cmdline cannot be parsed)

**Service label** from remote port:

| Remote port | Label  |
|-------------|--------|
| 80, 8080    | http   |
| 443, 8443   | https  |
| 5432        | pgsql  |
| 3306        | mysql  |
| 6379        | redis  |
| 27017       | mongo  |
| 6443        | k8s    |
| other       | port{N}|

**Sanitization**: lowercase, dots → dashes, non-alphanumeric/dash → removed, each label truncated to 63 characters.

**Collision handling**: if two active forwards produce the same name, append `-{localPort}` to the later one.

### Examples

```
ssh -L 127.0.0.1:8181:10.0.0.2:80   → 10-0-0-2-http.tunnel.test
ssh -L 127.0.0.1:5433:10.0.0.4:5432 → 10-0-0-4-pgsql.tunnel.test
kubectl port-forward svc/argocd-server 8080:443 → argocd-server-https.tunnel.test
kubectl port-forward pod/grafana-abc 3000:3000  → grafana-abc-port3000.tunnel.test
lad set 8181 myapp                             → myapp-http.tunnel.test
```

## Consequences

**Positive**
- Zero configuration required for the common case; names are descriptive out of the box.
- Service label adds protocol context without user input.
- Override mechanism covers cases where auto-generated names are unsatisfactory.
- Collision suffix (`-{localPort}`) ensures uniqueness without silent overwrites.
- Sanitization is deterministic and idempotent.

**Negative**
- Remote host IPs as identifiers (`10-0-0-4`) are numeric and opaque for users unfamiliar with the network topology.
- The service label table must be maintained as new services are added; unknown ports fall back to `port{N}`.
- Collision detection runs against the current active set in memory; domains are not guaranteed stable across daemon restarts if the set of active forwards changes order.
- Label truncation to 63 chars may produce collisions for long resource names; the `-{localPort}` suffix handles this but the truncated prefix might be confusing.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| `port-{localPort}` only | Opaque; no service or host context |
| `{remoteHost}` only | No service type; collision on same host, different port |
| `{remoteHost}-{remotePort}` | Numeric remote port in name is less readable than service label |
| Include namespace in kubectl names | Adds length; namespace often omitted in commands; secondary context |
| User-assigned names only (no auto) | Requires user configuration for every forward; breaks zero-config goal |
