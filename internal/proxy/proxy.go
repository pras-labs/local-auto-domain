package proxy

import (
	"crypto/tls"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
)

// Proxy listens on bindIP:listenPort and forwards all TCP connections to 127.0.0.1:targetPort.
// When tlsCert is set the listener presents TLS (*.tunnel.test cert) and the upstream
// connection uses TLS with InsecureSkipVerify, suitable for SSH-tunneled HTTPS services.
type Proxy struct {
	BindIP     string
	ListenPort int
	TargetPort int
	tlsCert    *tls.Certificate
	listener   net.Listener
	quit       chan struct{}
	wg         sync.WaitGroup
	stopOnce   sync.Once
}

func New(bindIP string, listenPort, targetPort int) *Proxy {
	return &Proxy{
		BindIP:     bindIP,
		ListenPort: listenPort,
		TargetPort: targetPort,
		quit:       make(chan struct{}),
	}
}

// NewTLS creates a proxy that terminates TLS from the client using cert and
// re-establishes TLS upstream (InsecureSkipVerify) to reach the tunneled service.
func NewTLS(bindIP string, listenPort, targetPort int, cert *tls.Certificate) *Proxy {
	return &Proxy{
		BindIP:     bindIP,
		ListenPort: listenPort,
		TargetPort: targetPort,
		tlsCert:    cert,
		quit:       make(chan struct{}),
	}
}

func (p *Proxy) Start() error {
	addr := p.BindIP + ":" + strconv.Itoa(p.ListenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if p.tlsCert != nil {
		ln = tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{*p.tlsCert},
		})
	}
	p.listener = ln
	p.wg.Add(1)
	go p.serve()
	return nil
}

func (p *Proxy) Stop() {
	p.stopOnce.Do(func() {
		close(p.quit)
		if p.listener != nil {
			if err := p.listener.Close(); err != nil {
				log.Printf("proxy %s:%d close listener: %v", p.BindIP, p.ListenPort, err)
			}
		}
	})
	p.wg.Wait()
}

func (p *Proxy) serve() {
	defer p.wg.Done()
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.quit:
			default:
				log.Printf("proxy %s:%d accept: %v", p.BindIP, p.ListenPort, err)
			}
			return
		}
		go p.pipe(conn)
	}
}

func (p *Proxy) pipe(src net.Conn) {
	defer src.Close()

	target := "127.0.0.1:" + strconv.Itoa(p.TargetPort)
	var dst net.Conn
	var err error

	if p.tlsCert != nil {
		//nolint:gosec // upstream is a local SSH tunnel; remote cert hostname won't match
		dst, err = tls.Dial("tcp", target, &tls.Config{InsecureSkipVerify: true})
	} else {
		dst, err = net.Dial("tcp", target)
	}
	if err != nil {
		log.Printf("proxy dial :%d: %v", p.TargetPort, err)
		return
	}
	defer dst.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(dst, src) }()
	go func() { defer wg.Done(); io.Copy(src, dst) }()
	wg.Wait()
}
