package ipc

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Entry represents one active port-forward domain mapping.
type Entry struct {
	Port       int       `json:"port"`
	RemoteHost string    `json:"remote_host"` // SSH: "10.0.0.2"; empty for kubectl
	RemotePort int       `json:"remote_port"`
	Resource   string    `json:"resource"`   // kubectl resource name; empty for SSH
	IP         string    `json:"ip"`         // assigned 127.0.1.X
	ProxyPort  int       `json:"proxy_port"` // port the proxy listens on
	Domain     string    `json:"domain"`
	Tool       string    `json:"tool"`
	TLS        bool      `json:"tls"`  // true when proxy terminates TLS with *.tunnel.test cert
	PID        int       `json:"pid"`
	Since      time.Time `json:"since"`
	Cmdline    string    `json:"cmdline"`
}

// StateStore holds in-memory daemon state.
type StateStore struct {
	entries map[int]Entry
}

func NewStateStore() *StateStore { return &StateStore{entries: make(map[int]Entry)} }

func (s *StateStore) Set(e Entry)     { s.entries[e.Port] = e }
func (s *StateStore) Delete(port int) { delete(s.entries, port) }
func (s *StateStore) All() []Entry {
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	return out
}

// Server exposes daemon state over a Unix domain socket.
type Server struct {
	store    *StateStore
	sockPath string
	listener net.Listener
}

func NewServer(store *StateStore) *Server {
	return &Server{store: store, sockPath: SocketPath()}
}

func (s *Server) Start() error {
	if err := os.MkdirAll(filepath.Dir(s.sockPath), 0755); err != nil {
		return err
	}
	os.Remove(s.sockPath)
	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return err
	}
	s.listener = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/state", s.handleState)
	go http.Serve(ln, mux) //nolint:errcheck
	return nil
}

func (s *Server) Stop() {
	if s.listener != nil {
		s.listener.Close()
	}
	os.Remove(s.sockPath)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.store.All())
}

func DataDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "local-auto-domain")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "local-auto-domain")
}

func SocketPath() string {
	return filepath.Join(DataDir(), "daemon.sock")
}
