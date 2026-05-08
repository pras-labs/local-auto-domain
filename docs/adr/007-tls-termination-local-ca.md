# ADR-007: TLS Termination at Proxy with Locally-Generated CA

**Status**: Accepted  
**Date**: 2026-05-06

## Context

When a port-forward targets a remote HTTPS service (remote port 443 or 8443), the TCP proxy must handle TLS correctly. The proxy receives a TLS `ClientHello` from the client and forwards bytes to `127.0.0.1:{localPort}`, which is the near end of the SSH tunnel carrying the remote TLS session.

The core problem is **TLS hostname mismatch**. When a client connects to `myapp-https.tunnel.test:8443`, it sends a TLS `ClientHello` with SNI `myapp-https.tunnel.test`. The remote server's certificate is for its own hostname (e.g., `api.example.com`). The client cannot verify the certificate because the SNI and the certificate subject do not match.

### Options evaluated

**Option A — Plain TCP passthrough (no TLS involvement)**  
The proxy copies bytes without inspecting them. The client performs the TLS handshake directly with the remote server through the tunnel.

Problem: The SNI sent by the client is `myapp-https.tunnel.test`. The remote server's cert subject is `api.example.com`. Certificate validation fails. `curl` needs `-k`; browsers show a warning. The domain name feature is broken for HTTPS services.

**Option B — TLS passthrough with SNI rewriting**  
Intercept the TLS `ClientHello`, rewrite the SNI to the original remote hostname, and forward the modified record.

Problem: SNI rewriting requires parsing and modifying TLS records, which is fragile and breaks TLS integrity in edge cases (session resumption, ESNI/ECH). Requires knowing the original remote hostname at the proxy level, which is available for SSH forwards but not always for kubectl.

**Option C — mkcert for certificate generation**  
Use the `mkcert` tool to generate a wildcard cert for `*.tunnel.test` and install its CA.

Problem: `mkcert` is an external binary dependency. Installation varies by platform. The daemon would need to shell out to `mkcert` and assume it is installed. Unsuitable for a self-contained tool.

**Option D — TLS termination with locally-generated CA (Go stdlib)**  
Generate a local ECDSA CA and a `*.tunnel.test` wildcard certificate using Go's `crypto/x509` and `crypto/tls` packages — no external tools. Install the CA into the system trust store during `lad setup`. The proxy terminates incoming TLS using this cert and re-establishes TLS to the upstream (local SSH tunnel port) with `InsecureSkipVerify: true`.

`InsecureSkipVerify` for the upstream connection is acceptable because:
- The upstream is `127.0.0.1:{localPort}` — a local loopback address, not a network path
- The SSH tunnel already provides transport encryption and host authentication to the remote
- The remote's TLS cert cannot be matched against `myapp-https.tunnel.test` anyway; requiring it would block all valid use cases

### Certificate requirements (macOS 10.15+ / iOS 13+)

Apple requires all TLS server certificates to:
- Include the DNS name in the **Subject Alternative Name** extension (not only in `CN`)
- Set `ExtKeyUsage: ServerAuth`
- Have `SubjectKeyId` on the CA and `AuthorityKeyId` on the leaf (used by macOS to build the trust chain from the system keychain)

The wildcard certificate is generated with all of these fields set correctly.

### Curl trust store divergence

On macOS, `security add-trusted-cert` installs the CA into the system keychain, which is used by:
- Safari, Chrome, Firefox
- `/usr/bin/curl` (Secure Transport)

It is **not** used by Homebrew-installed curl or any OpenSSL-based tool, which maintains its own CA bundle. Users of Homebrew curl must set `CURL_CA_BUNDLE=$(lad ca-cert)` in their shell profile. `lad setup` prints this instruction and `lad ca-cert` prints the CA path for scripting.

On Linux, `update-ca-certificates` adds the CA to the system-wide bundle used by both system and most third-party curl builds.

### Graceful degradation

If `lad setup` has not been run and no cert exists on disk, the daemon logs a warning and falls back to plain TCP passthrough for HTTPS services. This is the pre-setup behavior; no crash, no hard failure.

## Decision

For port-forwards targeting HTTPS services (`ServiceName(remotePort) == "https"`), use `proxy.NewTLS(bindIP, listenPort, targetPort, cert)`:
- Listener: `tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*cert}})`
- Upstream dial: `tls.Dial("tcp", target, &tls.Config{InsecureSkipVerify: true})`

Cert generation (`tlscert.EnsureCert`):
- ECDSA P-256 CA, 10-year validity, `SubjectKeyId` set
- ECDSA P-256 wildcard cert, 2-year validity, `DNSNames: [*.tunnel.test, tunnel.test]`, `AuthorityKeyId` set, `ExtKeyUsage: ServerAuth`
- Full chain (leaf + CA DER) embedded in `tls.Certificate.Certificate` so clients receive the chain without AIA lookup
- PEM files stored at `DataDir()/{ca,wildcard}.{crt,key}` with `0600` permissions

CA installation:
- macOS: `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain`
- Linux: copy to `/usr/local/share/ca-certificates/` + `sudo update-ca-certificates`

`lad uninstall` removes the CA from the trust store and deletes all cert files.

## Consequences

**Positive**
- HTTPS domains work in browsers and `/usr/bin/curl` without warnings after `lad setup`.
- Zero external tool dependencies; all crypto in Go stdlib.
- Wildcard cert covers all `*.tunnel.test` names without per-domain cert generation.
- Graceful fallback means existing HTTP workflows are unaffected if setup hasn't been run.
- `lad ca-cert` enables scripting: `curl --cacert $(lad ca-cert) https://...`

**Negative**
- The local CA is installed as a full trusted root in the system keychain. A compromised `ca.key` (requires local machine compromise — already game over) could be used to sign certs for any domain trusted by the system.
- Homebrew curl and other OpenSSL-based tools require a separate `CURL_CA_BUNDLE` configuration step.
- Certs must be regenerated every 2 years (daemon detects near-expiry and refuses to load expired certs; `lad setup` regenerates).
- `InsecureSkipVerify` upstream is a deliberate deviation from standard TLS validation, requiring clear documentation to avoid misinterpretation.

## Alternatives Rejected

| Option | Reason rejected |
|--------|----------------|
| Plain TCP passthrough | SNI mismatch causes cert validation failure in all clients |
| SNI rewriting | Fragile TLS record manipulation; breaks ESNI/ECH; incomplete remote hostname coverage |
| mkcert | External binary dependency; not self-contained |
| Per-domain certificates | Wildcard is sufficient; per-domain certs require CA re-invocation per forward |
| Self-signed cert per domain (no CA install) | Clients always warn; defeats the goal |
