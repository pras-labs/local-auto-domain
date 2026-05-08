# ADR-005: HTTP over Unix Domain Socket for Daemon–CLI IPC

**Status**: Accepted  
**Date**: 2026-05-06

## Context

`local-auto-domain` is structured as a long-running daemon (`lad daemon`) and a set of short-lived CLI commands (`lad list`, `lad status`). The CLI commands need to query the daemon's in-memory state (active port-forwards, assigned IPs, domains) without re-scanning the system themselves.

An IPC mechanism is needed that:
1. Works without network configuration (no port conflicts, no firewall issues)
2. Is local-only and not accessible remotely
3. Is simple to implement in Go with no additional dependencies
4. Supports structured data (JSON state)
5. Has client/server semantics (request/response, not pub/sub)

### Options considered

**TCP socket on localhost**  
Simple but occupies a port number, creating potential conflicts. Accessible to any process on the machine (including remote if firewall is misconfigured). Port selection is a coordination problem.

**Shared file / memory-mapped file**  
The daemon writes state to a JSON file periodically; CLI reads it. Simple but no request/response semantics, stale data between writes, and concurrent access needs locking. No way to check if daemon is alive without a separate mechanism.

**D-Bus**  
Standard Linux IPC but not available on macOS without additional software. Complex API. Not suitable for a portable tool.

**gRPC over Unix socket**  
Structured, typed, efficient. But requires a protobuf schema and the `google.golang.org/grpc` dependency — heavy for what is essentially one endpoint (`GET /state`).

**HTTP over Unix domain socket**  
Unix domain socket provides local-only, filesystem-permission-controlled access. HTTP provides a standard request/response framing that Go's `net/http` package supports natively over any `net.Listener`. No additional dependencies beyond stdlib.

### Why `net/http` over a Unix socket is sufficient

The IPC surface is one endpoint: `GET /state` returns a JSON array of `Entry` structs. This does not warrant a schema language or generated code. Go's `net/http` over a Unix socket is idiomatic, well-understood, and adds zero dependencies.

The socket path (`~/.local/share/local-auto-domain/daemon.sock` or `$XDG_RUNTIME_DIR/local-auto-domain/daemon.sock`) is owned and accessible only by the current user, providing access control for free.

### Daemon liveness detection

The CLI checks if the daemon is running by attempting to connect to the socket. A failed connect means the daemon is not running. This is simpler and more reliable than PID files.

## Decision

Use `net/http` served over a Unix domain socket at `SocketPath()` (derived from `DataDir()`). The daemon creates the socket on startup and removes it on shutdown. The CLI connects to it for all state queries.

Single endpoint:
- `GET /state` → `application/json` array of `Entry`

`ipc.Server` (daemon side) wraps `net/http.Serve` over a `net.Listen("unix", socketPath)` listener.  
`ipc.Client` (CLI side) uses `http.Client` with a custom `DialContext` that connects to the Unix socket path.

## Consequences

**Positive**
- Zero new dependencies; uses only `net`, `net/http`, and `encoding/json` from stdlib.
- Unix socket is local-only; no port allocation; no firewall rules.
- File permissions on the socket restrict access to the owning user.
- HTTP framing makes it easy to add future endpoints without changing the transport.
- Daemon liveness detection is a natural consequence of connection success/failure.

**Negative**
- Not accessible cross-machine (intentional, but worth noting for future multi-host scenarios).
- HTTP adds framing overhead that is unnecessary for a single-field protocol. Acceptable at this scale.
- Socket file must be cleaned up on abnormal daemon exit; stale sockets from crashes are handled by `os.Remove` before `net.Listen`.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| TCP localhost socket | Port conflict risk; remotely accessible by default |
| Shared JSON file | No liveness detection; stale data; concurrent access complexity |
| D-Bus | Not available on macOS without extra software |
| gRPC over Unix socket | Protobuf schema and dependency overhead not justified for one endpoint |
| Custom binary protocol | Unnecessary complexity; HTTP gives structure for free |
