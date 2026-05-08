package scanner

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// PortForward represents a detected port-forward LISTEN socket.
type PortForward struct {
	Port       int
	RemoteHost string // SSH: "10.0.0.2"; empty for kubectl
	RemotePort int    // original service port (80, 443, 5432, …)
	Resource   string // kubectl resource name: "argocd-server"; empty for SSH
	PID        int
	Cmdline    string
	Tool       string // "ssh" | "kubectl"
}

// Scanner detects active port-forward LISTEN sockets.
type Scanner interface {
	Scan() ([]PortForward, error)
}

// sshLRe matches any SSH combined-flag token that contains L (e.g. -L, -NL, -fNL).
var sshLRe = regexp.MustCompile(`-[a-zA-Z]*L`)

// ParseRemoteInfo extracts RemoteHost, RemotePort, and Resource from a cmdline
// given the local listened port.
func ParseRemoteInfo(tool, cmdline string, localPort int) (remoteHost string, remotePort int, resource string) {
	switch tool {
	case "ssh":
		remoteHost, remotePort = parseSSHInfo(cmdline, localPort)
	case "kubectl":
		resource, remotePort = parseKubectlInfo(cmdline, localPort)
	}
	return
}

// ClassifySSH returns true if cmdline is an ssh -L invocation (any flag combination).
func ClassifySSH(cmdline string) bool {
	return strings.Contains(strings.ToLower(cmdline), "ssh") && sshLRe.MatchString(cmdline)
}

// ClassifyKubectl returns true if cmdline is a kubectl port-forward invocation.
func ClassifyKubectl(cmdline string) bool {
	lower := strings.ToLower(cmdline)
	return strings.Contains(lower, "kubectl") && strings.Contains(lower, "port-forward")
}

// parseSSHInfo parses [bindaddr:]localPort:remoteHost:remotePort from SSH -L spec.
// Handles: -L spec, -Lspec, -NL spec, -fNL spec, etc.
func parseSSHInfo(cmdline string, localPort int) (host string, port int) {
	fields := strings.Fields(cmdline)
	for i, f := range fields {
		var spec string
		switch {
		case f == "-L" && i+1 < len(fields):
			spec = fields[i+1]
		case strings.HasPrefix(f, "-L") && len(f) > 2:
			spec = f[2:] // -Lspec
		case isSSHCombinedLFlag(f) && i+1 < len(fields):
			spec = fields[i+1] // -NL spec, -fNL spec
		default:
			continue
		}
		if h, p := sshSpecInfo(spec, localPort); p > 0 {
			return h, p
		}
	}
	return "", 0
}

// isSSHCombinedLFlag returns true for tokens like -NL, -fNL, -fNLW (all letters, contains L, not just -L).
func isSSHCombinedLFlag(f string) bool {
	if !strings.HasPrefix(f, "-") || f == "-L" {
		return false
	}
	flags := f[1:]
	for _, c := range flags {
		if !unicode.IsLetter(c) {
			return false
		}
	}
	return strings.ContainsRune(flags, 'L')
}

// sshSpecInfo parses [bindaddr:]localPort:remoteHost:remotePort.
// Returns ("", 0) if localPort doesn't match.
func sshSpecInfo(spec string, localPort int) (host string, port int) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 3: // localPort:host:remotePort
		lp, _ := strconv.Atoi(parts[0])
		rp, _ := strconv.Atoi(parts[2])
		if lp == localPort {
			return parts[1], rp
		}
	case 4: // bindaddr:localPort:host:remotePort
		lp, _ := strconv.Atoi(parts[1])
		rp, _ := strconv.Atoi(parts[3])
		if lp == localPort {
			return parts[2], rp
		}
	}
	return "", 0
}

// parseKubectlInfo extracts the resource name and remote port from a kubectl port-forward cmdline.
// kubectl [-n ns] [flags] port-forward (svc|pod|deploy)/name localPort:remotePort [...]
func parseKubectlInfo(cmdline string, localPort int) (resource string, remotePort int) {
	fields := strings.Fields(cmdline)
	for i, f := range fields {
		if f == "port-forward" && i+1 < len(fields) {
			resource = stripResourcePrefix(fields[i+1])
			break
		}
	}
	// Find localPort:remotePort pair
	re := regexp.MustCompile(`\b` + strconv.Itoa(localPort) + `:(\d+)\b`)
	if m := re.FindStringSubmatch(cmdline); len(m) >= 2 {
		remotePort, _ = strconv.Atoi(m[1])
	}
	return
}

// stripResourcePrefix removes "svc/", "pod/", "deployment/", "deploy/", etc.
func stripResourcePrefix(s string) string {
	if idx := strings.Index(s, "/"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}
