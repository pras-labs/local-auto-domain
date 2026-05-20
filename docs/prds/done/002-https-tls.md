# PRD 002: HTTPS/TLS Termination

**Status**: Done  
**Date**: 2026-05-07

---

## Problem

When a port-forward targets a remote HTTPS service, the proxy is a plain TCP passthrough. The client sends a TLS ClientHello with SNI set to the local domain (e.g., `myapp-https.tunnel.test`), but the remote cert is issued for the actual hostname (`myapp.internal`). Certificate validation fails:

```bash
ssh -L 127.0.0.1:8444:myapp.internal:443 user@host
curl https://myapp-https.tunnel.test:8443
# → curl: (60) SSL certificate problem: hostname mismatch
```

The workaround (`curl -k`) trains insecure habits and breaks browser access entirely.

---

## Goals

| Goal                      | Metric                                                                |
| ------------------------- | --------------------------------------------------------------------- |
| Trusted HTTPS             | `curl https://<domain>` returns 200 without `-k` after one-time setup |
| Green padlock in browsers | Safari, Chrome trust the cert without manual exception                |
| No external tools         | cert generation uses Go stdlib only (no mkcert)                       |
| Graceful degradation      | if setup not run, falls back to plain TCP — no daemon crash           |

---

## Solution

During `lad setup` (already requires sudo):

1. Generate an ECDSA P-256 local CA (`CN=local-auto-domain CA`, 10yr) using Go stdlib
2. Generate a wildcard leaf cert (`CN=*.tunnel.test`, SANs: `*.tunnel.test, tunnel.test`, 2yr)
3. Install the CA into the system trust store (macOS: `security add-trusted-cert`; Linux: copy + `update-ca-certificates`)
4. Store PEM files in the data dir at `0600`

When the daemon detects a forward targeting remote port 443 or 8443, it starts a TLS-terminating proxy instead of plain TCP:

- Incoming connections use `tls.NewListener` with the wildcard cert → client sees a trusted cert
- Upstream connection uses `tls.Dial(InsecureSkipVerify: true)` → connects through the SSH tunnel regardless of the remote cert hostname

**Traffic path**:

```plaintext
curl https://myapp-https.tunnel.test:8443
  → TLS handshake (*.tunnel.test cert, trusted) → proxy 127.0.1.X:8443
  → TLS upstream (InsecureSkipVerify) → 127.0.0.1:localPort
  → SSH tunnel → myapp.internal:443
```

`InsecureSkipVerify` is safe here: the SSH tunnel already provides transport security and authentication between the proxy and the remote host.

---

## Cert Generation Details

**CA**: ECDSA P-256, 10-year validity, `CN=local-auto-domain CA`, `IsCA=true`, `KeyUsageCertSign`.

**Wildcard cert**: ECDSA P-256, 2-year validity, SANs `*.tunnel.test` and `tunnel.test`, signed by the local CA.

**Rotation**: `LoadCert` rejects certs that are expiring within 30 days, missing `AuthorityKeyId`, or whose SANs don't include `*.tunnel.test`. Any rejection triggers automatic regeneration on the next `EnsureCert` call.

**Storage**: `~/.local/share/local-auto-domain/{ca.crt,ca.key,wildcard.crt,wildcard.key}` at `0600`.

---

## Trust Store Installation

| Platform | Mechanism                                                                         |
| -------- | --------------------------------------------------------------------------------- |
| macOS    | `security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain` |
| Linux    | copy CA to system CA bundle + `update-ca-certificates`                            |

**Homebrew curl / OpenSSL-based tools**: these link against their own CA bundle and do not use the system keychain. `lad setup` prints the env var needed: `CURL_CA_BUNDLE=~/.local/share/local-auto-domain/ca.crt`.

---

## Graceful Degradation

If `lad setup` has not been run (no cert files on disk), the daemon logs a warning and falls back to plain TCP passthrough for HTTPS forwards. `curl -k` still works; no crash.

---

## CLI Changes

`lad list` gains a TLS column:

```plaintext
PORT   DOMAIN                              IP          PROXY   TLS   TOOL      PID    SINCE
8444   10-0-0-5-https.tunnel.test          127.0.1.1   :8443   yes   ssh       1234   5m ago
8080   argocd-server-http.tunnel.test      127.0.1.2   :8080   no    kubectl   5678   12m ago
```

`lad ca-cert` prints the CA cert path for use with tools that need explicit trust.

---

## Data Storage

| Path                                            | Content                                  |
| ----------------------------------------------- | ---------------------------------------- |
| `~/.local/share/local-auto-domain/ca.crt`       | Local CA cert (install into trust store) |
| `~/.local/share/local-auto-domain/ca.key`       | Local CA private key (`0600`)            |
| `~/.local/share/local-auto-domain/wildcard.crt` | Wildcard leaf cert                       |
| `~/.local/share/local-auto-domain/wildcard.key` | Wildcard leaf private key (`0600`)       |

---

## Implementation Files

| File                                 | Purpose                                                                         |
| ------------------------------------ | ------------------------------------------------------------------------------- |
| `internal/tlscert/tlscert.go`        | `EnsureCert(dataDir)` — generate CA + wildcard cert, load and validate existing |
| `internal/tlscert/install_darwin.go` | `InstallCA(caFile)` via `security add-trusted-cert`                             |
| `internal/tlscert/install_linux.go`  | `InstallCA(caFile)` via copy + `update-ca-certificates`                         |
| `internal/proxy/proxy.go`            | `NewTLS(bindIP, listenPort, targetPort, cert)` — TLS listener + upstream dial   |
| `internal/daemon/daemon.go`          | Load cert at start; use `NewTLS` when remote port is 443 or 8443                |
| `internal/ipc/server.go`             | `TLS bool` field in `Entry`                                                     |
| `cmd/local-auto-domain/main.go`      | `setup`: call `EnsureCert` + `InstallCA`; `list`: TLS column; `ca-cert` command |

---

## Decision Records

| ADR                                              | Decision                                                                                     |
| ------------------------------------------------ | -------------------------------------------------------------------------------------------- |
| [007](../../adr/007-tls-termination-local-ca.md) | TLS termination with local CA + wildcard cert; Go stdlib only; `InsecureSkipVerify` upstream |
