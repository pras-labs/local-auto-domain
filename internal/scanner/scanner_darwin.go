package scanner

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type darwinScanner struct{}

func New() Scanner { return &darwinScanner{} }

func (s *darwinScanner) Scan() ([]PortForward, error) {
	out, err := exec.Command("lsof", "-iTCP", "-sTCP:LISTEN", "-P", "-n").Output()
	if err != nil {
		return nil, fmt.Errorf("lsof: %w", err)
	}

	type candidate struct {
		pid  int
		port int
	}
	var candidates []candidate

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		cmd := strings.ToLower(fields[0])
		if cmd != "ssh" && cmd != "kubectl" {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		port := parsePort(fields[8])
		if port <= 0 {
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

func parsePort(name string) int {
	idx := strings.LastIndex(name, ":")
	if idx < 0 {
		return 0
	}
	p, err := strconv.Atoi(name[idx+1:])
	if err != nil {
		return 0
	}
	return p
}

func classifyCmdline(pid int) (cmdline, tool string, ok bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", "", false
	}
	cmdline = strings.TrimSpace(string(out))
	if ClassifySSH(cmdline) {
		return cmdline, "ssh", true
	}
	if ClassifyKubectl(cmdline) {
		return cmdline, "kubectl", true
	}
	return "", "", false
}
