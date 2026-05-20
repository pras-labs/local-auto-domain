# local-auto-domain

Automatically generates resolvable `.tunnel.test` domain names for `ssh -L` and `kubectl port-forward` connections. Each forwarded port gets a unique domain and a TCP proxy bound to a dedicated loopback IP, so multiple forwards to the same service port are independently addressable by domain name. HTTPS services get a trusted wildcard TLS certificate — no browser warnings.

## Table of contents

- [How it works](#how-it-works)
- [Installation](#installation)
  - [Prerequisites](#prerequisites)
  - [Build from source](#build-from-source)
  - [One-time setup](#one-time-setup)
- [Usage](#usage)
  - [Start the daemon](#start-the-daemon)
  - [Install as a system service](#install-as-a-system-service-auto-start-on-login)
  - [List active mappings](#list-active-mappings)
  - [Override a domain identifier](#override-a-domain-identifier)
  - [Status](#status)
  - [Full uninstall](#full-uninstall)
- [HTTPS support](#https-support)
- [Domain naming](#domain-naming)
- [SSH flag support](#ssh-flag-support)
- [kubectl support](#kubectl-support)
- [Configuration](#configuration)
- [Architecture](#architecture)
- [Privilege model](#privilege-model)
- [Platforms](#platforms)
  - [Linux distros without systemd-resolved or systemd-networkd](#linux-distros-without-systemd-resolved-or-systemd-networkd)

## How it works

The daemon polls every 2 seconds for new port-forward processes. When one is detected:

1. A domain name is generated from the remote host/resource and service type
2. A unique IP is allocated from `127.0.1.0/24`
3. dnsmasq is configured to resolve that domain to that IP
4. A TCP proxy is started on `{uniqueIP}:{servicePort}` forwarding to `localhost:{localPort}`
   - For HTTPS services (remote port 443/8443): proxy terminates TLS using a locally-generated `*.tunnel.test` cert trusted by the system, then re-establishes TLS upstream

When the port-forward process exits, the domain, proxy, and IP are all cleaned up automatically.

### Example

```
ssh -fNL 127.0.0.1:8181:10.0.0.2:80   user@bastion
ssh -fNL 127.0.0.1:8282:10.0.0.3:80   user@bastion
ssh -fNL 127.0.0.1:5433:10.0.0.4:5432 user@bastion
ssh -fNL 127.0.0.1:8444:10.0.0.5:443  user@bastion
```

Results in:

| Domain                       | IP          | Proxy   | TLS | Forwards to      |
| ---------------------------- | ----------- | ------- | --- | ---------------- |
| `10-0-0-2-http.tunnel.test`  | `127.0.1.1` | `:8080` | no  | `localhost:8181` |
| `10-0-0-3-http.tunnel.test`  | `127.0.1.2` | `:8080` | no  | `localhost:8282` |
| `10-0-0-4-pgsql.tunnel.test` | `127.0.1.3` | `:5432` | no  | `localhost:5433` |
| `10-0-0-5-https.tunnel.test` | `127.0.1.4` | `:8443` | yes | `localhost:8444` |

Access with:

```
curl http://10-0-0-2-http.tunnel.test:8080
psql -h 10-0-0-4-pgsql.tunnel.test
curl https://10-0-0-5-https.tunnel.test:8443   # trusted cert, no -k needed
```

## Installation

### Prerequisites

- **macOS**: Homebrew
- **Linux**: apt, dnf, or pacman

### Build from source

```bash
git clone https://github.com/pras-labs/local-auto-domain
cd local-auto-domain
go build -o lad ./cmd/local-auto-domain
sudo mv lad /usr/local/bin/
```

### One-time setup

```bash
lad setup
```

This performs all first-run configuration:

- Installs and configures dnsmasq
- Writes `/etc/resolver/test` (macOS) or systemd-resolved split-DNS (Linux)
- Creates loopback aliases `127.0.1.1–127.0.1.100` and installs a boot LaunchDaemon to recreate them (macOS only)
- Generates a local CA and wildcard TLS certificate for `*.tunnel.test`
- Installs the CA into the system trust store so browsers and curl trust the cert

## Usage

### Start the daemon

```bash
lad daemon
```

### Install as a system service (auto-start on login)

```bash
lad install-service    # launchd on macOS, systemd --user on Linux
lad uninstall-service
```

### List active mappings

```bash
lad list
```

```
PORT   DOMAIN                              IP          PROXY   TLS   TOOL      PID    SINCE
8444   10-0-0-5-https.tunnel.test     127.0.1.1   :8443   yes   ssh       1234   5m ago
8080   argocd-server-http.tunnel.test 127.0.1.2   :8080   no    kubectl   5678   12m ago
5433   10-0-0-4-pgsql.tunnel.test     127.0.1.3   :5432   no    ssh       9012   1m ago
```

### Override a domain identifier

```bash
lad set 8181 myapp        # 10-0-0-2-http.tunnel.test → myapp-http.tunnel.test
lad unset 8181
```

Restart the daemon after changing overrides.

### Status

```bash
lad status
```

### Full uninstall

```bash
lad uninstall
```

Removes everything `lad setup` created:

- Stops and removes the system service
- Removes dnsmasq drop-in config and DNS resolver entries
- Removes loopback aliases and their boot LaunchDaemon (macOS)
- Removes the local CA from the system trust store
- Deletes cert files and the data directory
- Deletes the config file

## HTTPS support

When a port-forward targets a remote HTTPS service (remote port 443 or 8443), the proxy performs TLS termination:

```
curl https://myapp-https.tunnel.test:8443
  → TLS (*.tunnel.test cert, trusted) → proxy 127.0.1.X:8443
  → TLS upstream (local tunnel) → 127.0.0.1:localPort
  → SSH tunnel → remote:443
```

The `*.tunnel.test` wildcard cert is generated by `lad setup` using Go's standard crypto library — no mkcert or other external tools required. The CA is stored at `~/.local/share/local-auto-domain/ca.crt` and installed into the system keychain.

If `lad setup` has not been run, HTTPS forwards fall back to plain TCP passthrough (same behavior as before setup — curl needs `-k`).

## Domain naming

Domains follow the pattern `{identifier}-{service}.tunnel.test`.

**Identifier** (in priority order):

1. Override from config (`lad set <port> <name>`)
2. SSH: remote host with dots replaced by dashes (`10.0.0.2` → `10-0-0-2`)
3. kubectl: resource name from `svc/name`, `pod/name`, etc.
4. Fallback: `port-{localPort}`

**Service name** from the remote port:

| Remote port | Service label |
| ----------- | ------------- |
| 80          | http          |
| 443         | https         |
| 5432        | pgsql         |
| 3306        | mysql         |
| 6379        | redis         |
| 27017       | mongo         |
| 6443        | k8s           |
| other       | port{N}       |

If two active forwards would produce the same domain name, the local port is appended as a suffix.

## SSH flag support

All common `ssh -L` flag combinations are supported:

```bash
ssh -L 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -NL 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -fNL 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -fNLW 127.0.0.1:8181:10.0.0.2:80 user@host
ssh -L8181:10.0.0.2:80 user@host
```

## kubectl support

```bash
kubectl port-forward svc/myapp 8080:80
kubectl -n monitoring port-forward pod/grafana-abc 3000:3000
kubectl port-forward svc/postgres 5432:5432
```

## Configuration

Config file: `~/.config/local-auto-domain/config.yaml` (Linux) or `~/Library/Application Support/local-auto-domain/config.yaml` (macOS).

```yaml
poll_interval: 2s
tld: tunnel.test

# Override domain identifier per local port
overrides:
  8181: myapp
  5433: prod-db

# Proxy listen port per service type.
# Change these if the defaults conflict with locally running services.
service_ports:
  http: 8080
  https: 8443
  pgsql: 5432
  mysql: 3306
  redis: 6379
  mongo: 27017
  k8s: 6443
```

## Architecture

```
local-auto-domain/
├── cmd/local-auto-domain/    # CLI entry point (cobra)
└── internal/
    ├── scanner/              # Detect ssh/kubectl LISTEN sockets (lsof/ss)
    │   ├── scanner.go        # Interface, shared SSH/kubectl parsing
    │   ├── scanner_darwin.go # lsof-based
    │   └── scanner_linux.go  # ss + /proc-based
    ├── daemon/               # Poll loop, lifecycle orchestration
    ├── domain/               # Domain name generation and sanitization
    ├── config/               # YAML config with service port mappings
    ├── ipalloc/              # 127.0.1.1–254 IP pool
    ├── netutil/              # Loopback alias management (macOS/Linux)
    ├── dnsmasq/              # dnsmasq config file writer + setup/teardown
    ├── proxy/                # TCP proxy (bindIP:port → localhost:targetPort); TLS mode for HTTPS
    ├── tlscert/              # Local CA + wildcard cert generation; system trust store install/remove
    ├── ipc/                  # Unix socket server/client (daemon ↔ CLI)
    └── service/              # launchd / systemd unit install
```

**IPC**: Daemon exposes `GET /state` over a Unix socket at `~/.local/share/local-auto-domain/daemon.sock` (mode `0600`, directory `0700` — owner access only). CLI commands talk to it directly.

**DNS**: The daemon maintains a single `hosts` file at `{dnsmasq.d}/local-auto-domain/hosts` registered with dnsmasq via `addn-hosts`. dnsmasq re-reads `addn-hosts` files on `SIGHUP` — no restart needed. (`conf-dir` entries are startup-only and are not re-read on SIGHUP; per-port `port-N.conf` state files exist solely for daemon-restart recovery.) On macOS, dnsmasq runs on port 5300 as a user-level LaunchAgent so the daemon can send SIGHUP without sudo. On Linux, a NOPASSWD sudoers rule written by `lad setup` allows `sudo systemctl reload dnsmasq` without a prompt. dnsmasq is pinned to `listen-address=127.0.0.1` + `bind-interfaces` to avoid conflicting with systemd-resolved's stub listener on `127.0.0.53:53`.

**Linux DNS routing**: `lad setup` creates a `lad-dns` dummy network interface via systemd-networkd with `DNS=127.0.0.1` and `Domains=~tunnel.test`. This gives systemd-resolved an isolated per-link DNS scope — `tunnel.test` queries go exclusively to dnsmasq; DHCP-provided public DNS servers are unaffected. The global resolved drop-in approach (`DNS=` in `resolved.conf.d`) does not work because DHCP servers are pooled in the same scope and the "current" server (e.g. `1.1.1.1`) wins. `lad setup` detects the active network manager (systemd-networkd, NetworkManager, dhcpcd, connman, ifupdown) and configures or guides accordingly.

**Routing**: Unique loopback IPs (`127.0.1.X`) mean multiple domains can share the same proxy port (e.g., two HTTP services both accessible on `:8080` via different IPs). Works for any TCP protocol — HTTP, PostgreSQL, Redis, etc.

**TLS**: The `tlscert` package generates an ECDSA P-256 local CA and a `*.tunnel.test` wildcard cert using Go's standard library. Keys are stored at `~/.local/share/local-auto-domain/` with `0600` permissions. The proxy uses `tls.NewListener` for incoming connections and `tls.Dial` with `InsecureSkipVerify` for the upstream tunnel connection (the SSH tunnel already provides transport security).

## Privilege model

| Operation                       | Privilege needed                                          |
| ------------------------------- | --------------------------------------------------------- |
| `lad setup`                     | user — prompts for password when performing system ops    |
| `lad daemon`                    | user                                                      |
| `lad install-service`           | user                                                      |
| `lad uninstall`                 | user — prompts for password when removing system CA/aliases |
| Runtime dnsmasq updates (macOS) | user — dnsmasq runs as user LaunchAgent on port 5300      |
| Runtime dnsmasq updates (Linux) | `sudo systemctl reload dnsmasq` via NOPASSWD sudoers rule |
| Loopback aliases (macOS)        | created by LaunchDaemon at boot; no runtime sudo          |

## Platforms

|                     | macOS                            | Linux                                                          |
| ------------------- | -------------------------------- | -------------------------------------------------------------- |
| Socket detection    | `lsof`                           | `ss` + `/proc`                                                 |
| DNS resolver config | `/etc/resolver/test` (port 5300) | `lad-dns` dummy interface (networkd + resolved per-link scope) |
| Loopback aliases    | `ifconfig lo0 alias` (setup)     | not needed (127.0.0.0/8 routable)                              |
| Service manager     | launchd                          | systemd --user                                                 |
| CA trust store      | system keychain (`security`)     | `update-ca-certificates`                                       |

### Linux distros without systemd-resolved or systemd-networkd

`lad setup` configures split-DNS via a `lad-dns` dummy interface managed by systemd-networkd, with systemd-resolved providing per-domain query routing. Both must be active. On distros that don't use them (Alpine, minimal Debian/Ubuntu, some container images), the split-DNS step is skipped and `*.tunnel.test` queries won't reach dnsmasq.

Check whether systemd-resolved is active:

```bash
systemctl is-active systemd-resolved
```

If the unit is inactive, enable it:

```bash
sudo systemctl enable --now systemd-resolved
sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
```

Then re-run `lad setup`.

If the unit file does not exist (e.g. Debian Bookworm minimal/server install), install it first:

```bash
sudo apt install systemd-resolved
sudo systemctl enable --now systemd-resolved
sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
```

Then re-run `lad setup`. If you'd rather not install systemd-resolved, choose one of the manual approaches below.

#### Option A: dnsmasq handles all DNS (simple, intrusive)

Tell the system to use dnsmasq as its only resolver. Requires editing `/etc/resolv.conf` or your distro's network config.

```bash
# Make dnsmasq listen on localhost (already true after lad setup)
# Point the system resolver at it
echo "nameserver 127.0.0.1" | sudo tee /etc/resolv.conf
```

On systems managed by NetworkManager, prevent it from overwriting `resolv.conf`:

```bash
# /etc/NetworkManager/NetworkManager.conf
[main]
dns=none
```

**Trade-off**: all DNS queries go through dnsmasq. If dnsmasq is down, DNS stops. Upstream forwarding must be configured in `/etc/dnsmasq.conf`:

```
# Forward everything except tunnel.test to your upstream resolver
server=8.8.8.8
server=8.8.4.4
```

#### Option B: dnsmasq answers only `.tunnel.test` (surgical, no system-wide change)

Add a routing-only entry to dnsmasq and point only `.tunnel.test` lookups at it. Works with any resolver that supports per-domain forwarding.

**With NetworkManager + dnsmasq plugin:**

```bash
# /etc/NetworkManager/dnsmasq.d/tunnel-test.conf
server=/tunnel.test/127.0.0.1
```

```bash
sudo systemctl restart NetworkManager
```

**With resolvconf / openresolv:**

```bash
# /etc/resolvconf/resolv.conf.d/head
nameserver 127.0.0.1
```

Then add to `/etc/dnsmasq.conf`:

```
server=/tunnel.test/127.0.0.1
```

```bash
sudo systemctl restart dnsmasq
```

**Verify either option with:**

```bash
# Should return 127.0.1.X (not NXDOMAIN)
getent hosts any-name.tunnel.test
```

> **Note**: `host`, `dig`, and `nslookup` query `/etc/resolv.conf` directly and bypass per-domain routing. Use `getent hosts` or `curl` to verify — these go through the full resolver stack.
