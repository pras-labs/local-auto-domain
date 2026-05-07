package netutil

import "net"

// EnsureAlias is a no-op on Linux: all 127.0.0.0/8 addresses are routable on lo.
func EnsureAlias(ip net.IP) error { return nil }

// RemoveAlias is a no-op on Linux.
func RemoveAlias(ip net.IP) error { return nil }
