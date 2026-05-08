package scanner

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type linuxScanner struct{}

func New() Scanner { return &linuxScanner{} }

func (s *linuxScanner) Scan() ([]PortForward, error) {
	out, err := exec.Command("ss", "-tlnp").Output()
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}

	type candidate struct {
		pid  int
		port int
	}
	var candidates []candidate

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "State") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		localAddr := fields[3]
		if !isLoopback(localAddr) {
			continue
		}
		port := parsePort(localAddr)
		if port <= 0 {
			continue
		}
		procField := fields[5]
		if !strings.Contains(procField, "kubectl") && !strings.Contains(procField, "ssh") {
			continue
		}
		pid := parsePIDFromSS(procField)
		if pid <= 0 {
			continue
		}
		candidates = append(candidates, candidate{pid, port})
	}

	var result []PortForward
	for _, c := range candidates {
		cmdline, tool, ok := classifyCmdline(c.pid)
		if !ok {
			continue
		}
		remoteHost, remotePort, resource := ParseRemoteInfo(tool, cmdline, c.port)
		result = append(result, PortForward{
			Port:       c.port,
			RemoteHost: remoteHost,
			RemotePort: remotePort,
			Resource:   resource,
			PID:        c.pid,
			Cmdline:    cmdline,
			Tool:       tool,
		})
	}
	return result, nil
}

func isLoopback(addr string) bool {
	return strings.HasPrefix(addr, "127.") ||
		strings.HasPrefix(addr, "[::1]") ||
		strings.HasPrefix(addr, "::1")
}

func parsePort(addr string) int {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return 0
	}
	p, _ := strconv.Atoi(addr[idx+1:])
	return p
}

func parsePIDFromSS(field string) int {
	const prefix = "pid="
	idx := strings.Index(field, prefix)
	if idx < 0 {
		return 0
	}
	rest := field[idx+len(prefix):]
	end := strings.IndexAny(rest, ",)")
	if end < 0 {
		end = len(rest)
	}
	pid, _ := strconv.Atoi(rest[:end])
	return pid
}

func classifyCmdline(pid int) (cmdline, tool string, ok bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", "", false
	}
	cmdline = strings.ReplaceAll(string(raw), "\x00", " ")
	cmdline = strings.TrimSpace(cmdline)
	if ClassifySSH(cmdline) {
		return cmdline, "ssh", true
	}
	if ClassifyKubectl(cmdline) {
		return cmdline, "kubectl", true
	}
	return "", "", false
}
