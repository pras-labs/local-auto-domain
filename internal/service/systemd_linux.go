package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const unitName = "local-auto-domain.service"

func unitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", unitName)
}

func Install(binaryPath string) error {
	unit := fmt.Sprintf(`[Unit]
Description=local-auto-domain daemon
After=network.target

[Service]
ExecStart=%s daemon
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, binaryPath)

	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(unit), 0644); err != nil {
		return err
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", unitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %w\n%s", err, out)
	}
	fmt.Printf("Service installed: %s\nLogs: journalctl --user -u %s\n", unitName, unitName)
	return nil
}

func Uninstall() error {
	exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Printf("Service removed: %s\n", unitName)
	return nil
}

func Status() (string, error) {
	out, err := exec.Command("systemctl", "--user", "status", unitName).Output()
	if err != nil {
		return "not installed", nil
	}
	return string(out), nil
}
