package ipalloc

import (
	"fmt"
	"net"
	"sync"
)

// Allocator hands out unique addresses from the 127.0.1.1–127.0.1.254 range.
type Allocator struct {
	used map[byte]bool
	mu   sync.Mutex
}

func New() *Allocator {
	return &Allocator{used: make(map[byte]bool)}
}

// Allocate returns the next free 127.0.1.X address.
func (a *Allocator) Allocate() (net.IP, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := byte(1); i < 255; i++ {
		if !a.used[i] {
			a.used[i] = true
			return net.IPv4(127, 0, 1, i).To4(), nil
		}
	}
	return nil, fmt.Errorf("ip pool exhausted (>254 active port-forwards)")
}

// Free returns an address to the pool.
func (a *Allocator) Free(ip net.IP) {
	if ip == nil {
		return
	}
	ip4 := ip.To4()
	if ip4 == nil || ip4[0] != 127 || ip4[1] != 0 || ip4[2] != 1 {
		return
	}
	a.mu.Lock()
	delete(a.used, ip4[3])
	a.mu.Unlock()
}
