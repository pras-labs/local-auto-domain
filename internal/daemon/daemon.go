package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/pras-labs/local-auto-domain/internal/config"
	"github.com/pras-labs/local-auto-domain/internal/dnsmasq"
	"github.com/pras-labs/local-auto-domain/internal/domain"
	"github.com/pras-labs/local-auto-domain/internal/ipc"
	"github.com/pras-labs/local-auto-domain/internal/ipalloc"
	"github.com/pras-labs/local-auto-domain/internal/netutil"
	"github.com/pras-labs/local-auto-domain/internal/proxy"
	"github.com/pras-labs/local-auto-domain/internal/scanner"
	"github.com/pras-labs/local-auto-domain/internal/tlscert"
)

type activeEntry struct {
	ipc.Entry
	ip    net.IP
	proxy *proxy.Proxy
}

// Daemon polls for port-forward processes and manages dnsmasq entries, TCP proxies, and loopback IPs.
type Daemon struct {
	cfg     *config.Config
	scanner scanner.Scanner
	dns     *dnsmasq.Manager
	alloc   *ipalloc.Allocator
	store   *ipc.StateStore
	ipcSrv  *ipc.Server
	active  map[int]*activeEntry
	tlsCert *tls.Certificate // nil when setup not yet run
}

func New(cfg *config.Config, sc scanner.Scanner) *Daemon {
	store := ipc.NewStateStore()
	d := &Daemon{
		cfg:    cfg,
		scanner: sc,
		dns:    dnsmasq.New(dnsmasq.DropInDir()),
		alloc:  ipalloc.New(),
		store:  store,
		ipcSrv: ipc.NewServer(store),
		active: make(map[int]*activeEntry),
	}
	if cert, err := tlscert.LoadCert(ipc.DataDir()); err == nil {
		d.tlsCert = cert
		log.Println("TLS cert loaded: HTTPS services will use trusted *.tunnel.test cert")
	} else {
		log.Printf("TLS cert not available (run 'lad setup' to enable HTTPS termination): %v", err)
	}
	return d
}

func (d *Daemon) Run(ctx context.Context) error {
	if err := d.ipcSrv.Start(); err != nil {
		return fmt.Errorf("IPC server: %w", err)
	}
	defer d.ipcSrv.Stop()

	log.Printf("local-auto-domain daemon started (poll: %s)", d.cfg.PollInterval)

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	d.poll()

	for {
		select {
		case <-ctx.Done():
			d.cleanup()
			return nil
		case <-ticker.C:
			d.poll()
		}
	}
}

func (d *Daemon) poll() {
	forwards, err := d.scanner.Scan()
	if err != nil {
		log.Printf("scan error: %v", err)
		return
	}

	current := make(map[int]scanner.PortForward, len(forwards))
	for _, pf := range forwards {
		current[pf.Port] = pf
	}

	for port, pf := range current {
		if _, exists := d.active[port]; !exists {
			d.add(pf)
		}
	}

	for port := range d.active {
		if _, exists := current[port]; !exists {
			d.remove(port)
		}
	}
}

func (d *Daemon) add(pf scanner.PortForward) {
	// Build set of currently active domain names (without TLD) for collision check
	activeNames := make(map[string]struct{}, len(d.active))
	for _, e := range d.active {
		name := e.Domain
		if idx := len(name) - len(d.cfg.TLD) - 1; idx > 0 {
			name = name[:idx]
		}
		activeNames[name] = struct{}{}
	}

	name := domain.Generate(pf, d.cfg, activeNames)

	ip, err := d.alloc.Allocate()
	if err != nil {
		log.Printf("ip alloc: %v", err)
		return
	}

	if err := netutil.EnsureAlias(ip); err != nil {
		log.Printf("loopback alias %s: %v (continuing anyway)", ip, err)
	}

	if err := d.dns.Add(pf.Port, name, ip.String()); err != nil {
		log.Printf("dnsmasq add port %d: %v", pf.Port, err)
		d.alloc.Free(ip)
		return
	}

	proxyPort := d.cfg.ServiceProxyPort(pf.RemotePort)
	isHTTPS := config.ServiceName(pf.RemotePort) == "https"
	useTLS := isHTTPS && d.tlsCert != nil

	var p *proxy.Proxy
	if useTLS {
		p = proxy.NewTLS(ip.String(), proxyPort, pf.Port, d.tlsCert)
	} else {
		p = proxy.New(ip.String(), proxyPort, pf.Port)
	}
	if err := p.Start(); err != nil {
		log.Printf("proxy %s:%d→:%d: %v", ip, proxyPort, pf.Port, err)
		p = nil
	}

	entry := ipc.Entry{
		Port:       pf.Port,
		RemoteHost: pf.RemoteHost,
		RemotePort: pf.RemotePort,
		Resource:   pf.Resource,
		IP:         ip.String(),
		ProxyPort:  proxyPort,
		Domain:     name,
		Tool:       pf.Tool,
		TLS:        useTLS,
		PID:        pf.PID,
		Since:      time.Now(),
		Cmdline:    pf.Cmdline,
	}
	d.active[pf.Port] = &activeEntry{Entry: entry, ip: ip, proxy: p}
	d.store.Set(entry)

	tlsLabel := ""
	if useTLS {
		tlsLabel = " [TLS]"
	}
	log.Printf("added: %s → %s:%d%s (local :%d, %s)", name, ip, proxyPort, tlsLabel, pf.Port, pf.Tool)
}

func (d *Daemon) remove(port int) {
	e := d.active[port]
	if e.proxy != nil {
		e.proxy.Stop()
	}
	d.dns.Remove(port)
	netutil.RemoveAlias(e.ip)
	d.alloc.Free(e.ip)
	delete(d.active, port)
	d.store.Delete(port)
	log.Printf("removed: %s (:%d)", e.Domain, port)
}

func (d *Daemon) cleanup() {
	log.Println("daemon stopping, cleaning up...")
	for port := range d.active {
		d.remove(port)
	}
}
