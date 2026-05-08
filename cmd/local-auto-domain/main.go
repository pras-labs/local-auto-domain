package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pras-labs/local-auto-domain/internal/config"
	"github.com/pras-labs/local-auto-domain/internal/daemon"
	"github.com/pras-labs/local-auto-domain/internal/dnsmasq"
	"github.com/pras-labs/local-auto-domain/internal/ipc"
	"github.com/pras-labs/local-auto-domain/internal/scanner"
	"github.com/pras-labs/local-auto-domain/internal/service"
	"github.com/pras-labs/local-auto-domain/internal/tlscert"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:          "local-auto-domain",
		Short:        "Auto-generate .local.dev domains for port-forwards",
		SilenceUsage: true,
	}

	root.AddCommand(
		cmdDaemon(),
		cmdSetup(),
		cmdUninstall(),
		cmdCACert(),
		cmdList(),
		cmdStatus(),
		cmdSet(),
		cmdUnset(),
		cmdInstallService(),
		cmdUninstallService(),
		cmdVersion(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func cmdDaemon() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Start the port-forward watcher daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			sc := scanner.New()
			d := daemon.New(cfg, sc)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			return d.Run(ctx)
		},
	}
}

func cmdSetup() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install and configure dnsmasq + TLS (run once, may require sudo)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dnsmasq.Setup(); err != nil {
				return err
			}
			fmt.Println("Generating TLS certificate for *.tunnel.test...")
			_, caFile, err := tlscert.EnsureCert(ipc.DataDir())
			if err != nil {
				return fmt.Errorf("generating TLS cert: %w", err)
			}
			fmt.Println("Installing CA into system keychain (requires sudo)...")
			if err := tlscert.InstallCA(caFile); err != nil {
				fmt.Printf("Warning: CA installation failed: %v\n", err)
				fmt.Println("Browsers may still show certificate warnings.")
			} else {
				fmt.Println("CA installed (browsers + /usr/bin/curl).")
			}
			fmt.Println()
			fmt.Println("Homebrew curl and other OpenSSL-based tools ignore the system keychain.")
			fmt.Println("Add to ~/.zshrc or ~/.bashrc to trust the cert there too:")
			fmt.Printf("  export CURL_CA_BUNDLE=%s\n", caFile)
			fmt.Println()
			fmt.Printf("CA path: %s  (run 'lad ca-cert' to print this)\n", caFile)
			return nil
		},
	}
}

func cmdCACert() *cobra.Command {
	return &cobra.Command{
		Use:   "ca-cert",
		Short: "Print path to the local CA certificate",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(filepath.Join(ipc.DataDir(), "ca.crt"))
		},
	}
}

func cmdUninstall() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove all local-auto-domain configuration and system changes",
		Long: `Removes everything installed by 'lad setup':
  - Stops and removes the system service (if installed)
  - Removes dnsmasq drop-in config and /etc/resolver/test
  - Removes loopback aliases and their boot LaunchDaemon (macOS)
  - Removes the local CA from the system trust store
  - Deletes cert files and the data directory
  - Deletes the config file`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Stopping and removing service...")
			if err := service.Uninstall(); err != nil {
				fmt.Printf("  warning: %v\n", err)
			}

			fmt.Println("Removing dnsmasq configuration (may require sudo)...")
			if err := dnsmasq.Teardown(); err != nil {
				fmt.Printf("  warning: %v\n", err)
			}

			fmt.Println("Removing CA from system trust store (requires sudo)...")
			caFile := filepath.Join(ipc.DataDir(), "ca.crt")
			if err := tlscert.RemoveCA(caFile); err != nil {
				fmt.Printf("  warning: %v\n", err)
			}

			dataDir := ipc.DataDir()
			fmt.Printf("Removing data directory %s...\n", dataDir)
			os.RemoveAll(dataDir)

			cfgPath := config.Path()
			fmt.Printf("Removing config file %s...\n", cfgPath)
			os.Remove(cfgPath)
			os.Remove(filepath.Dir(cfgPath)) // remove dir only if empty; ignore error

			fmt.Println("Uninstall complete.")
			return nil
		},
	}
}

func cmdList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active port-forward domain mappings",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := ipc.NewClient()
			entries, err := client.GetState()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Println("No active port-forwards detected.")
				return nil
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Port < entries[j].Port })
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "PORT\tDOMAIN\tIP\tPROXY\tTLS\tTOOL\tPID\tSINCE")
			for _, e := range entries {
				proxyCol := fmt.Sprintf(":%d", e.ProxyPort)
				tlsCol := "no"
				if e.TLS {
					tlsCol = "yes"
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
					e.Port, e.Domain, e.IP, proxyCol, tlsCol, e.Tool, e.PID, ago(e.Since))
			}
			return w.Flush()
		},
	}
}

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon and dnsmasq status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := ipc.NewClient()
			if client.IsDaemonRunning() {
				entries, _ := client.GetState()
				fmt.Printf("Daemon:  running\nActive:  %d domain(s)\n", len(entries))
			} else {
				fmt.Println("Daemon:  stopped")
			}
			svcStatus, _ := service.Status()
			fmt.Printf("Service: %s\n", svcStatus)
			return nil
		},
	}
}

func cmdSet() *cobra.Command {
	return &cobra.Command{
		Use:   "set <port> <name>",
		Short: "Override domain name for a port (e.g. set 8080 myapp)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid port: %s", args[0])
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.SetOverride(port, args[1]); err != nil {
				return err
			}
			fmt.Printf("Override set: port %d → %s.%s\n", port, args[1], cfg.TLD)
			fmt.Println("Restart the daemon for changes to take effect.")
			return nil
		},
	}
}

func cmdUnset() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <port>",
		Short: "Remove domain name override for a port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid port: %s", args[0])
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.RemoveOverride(port); err != nil {
				return err
			}
			fmt.Printf("Override removed for port %d.\n", port)
			return nil
		},
	}
}

func cmdInstallService() *cobra.Command {
	return &cobra.Command{
		Use:   "install-service",
		Short: "Install daemon as a system service (auto-start on login)",
		RunE: func(cmd *cobra.Command, args []string) error {
			bin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("could not determine binary path: %w", err)
			}
			return service.Install(bin)
		},
	}
}

func cmdUninstallService() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall-service",
		Short: "Remove the system service",
		RunE: func(cmd *cobra.Command, args []string) error {
			return service.Uninstall()
		},
	}
}

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("local-auto-domain %s\n", version)
		},
	}
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}
