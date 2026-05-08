package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/pras-labs/local-auto-domain/internal/config"
	"github.com/pras-labs/local-auto-domain/internal/scanner"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Generate returns a collision-safe domain name for pf.
// activeNames is the set of names currently in use (without TLD).
func Generate(pf scanner.PortForward, cfg *config.Config, activeNames map[string]struct{}) string {
	base := baseIdentifier(pf, cfg)
	svc := serviceLabel(pf.RemotePort)
	name := sanitize(base + "-" + svc)

	// Collision: two forwards to same remote host+service get localPort suffix
	if _, taken := activeNames[name]; taken {
		name = sanitize(fmt.Sprintf("%s-%s-%d", base, svc, pf.Port))
	}
	return name + "." + cfg.TLD
}

func baseIdentifier(pf scanner.PortForward, cfg *config.Config) string {
	if override, ok := cfg.Overrides[pf.Port]; ok {
		return override
	}
	if pf.RemoteHost != "" {
		return pf.RemoteHost // dots become dashes in sanitize()
	}
	if pf.Resource != "" {
		return pf.Resource
	}
	return fmt.Sprintf("port-%d", pf.Port)
}

func serviceLabel(remotePort int) string {
	if svc := config.ServiceName(remotePort); svc != "" {
		return svc
	}
	return fmt.Sprintf("port%d", remotePort)
}

// sanitize lowercases, replaces non-alnum runs with dashes, trims dashes,
// and truncates each DNS label to 63 chars.
func sanitize(s string) string {
	s = strings.ToLower(s)
	// Replace non-alphanumeric chars (including dots) with dashes
	var b strings.Builder
	prev := '-'
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prev = r
		} else if prev != '-' {
			b.WriteByte('-')
			prev = '-'
		}
	}
	result := strings.Trim(b.String(), "-")
	if len(result) > 63 {
		result = strings.TrimRight(result[:63], "-")
	}
	return result
}
