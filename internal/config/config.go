package config

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// defaultServicePorts maps service names to proxy listen ports.
var defaultServicePorts = map[string]int{
	"http":  8080,
	"https": 8443,
	"pgsql": 5432,
	"mysql": 3306,
	"redis": 6379,
	"mongo": 27017,
	"k8s":   6443,
}

// serviceNameByPort maps remote port numbers to service names.
var serviceNameByPort = map[int]string{
	80:    "http",
	443:   "https",
	5432:  "pgsql",
	3306:  "mysql",
	6379:  "redis",
	27017: "mongo",
	6443:  "k8s",
	8080:  "http",
	8443:  "https",
}

type Config struct {
	PollInterval time.Duration  `yaml:"poll_interval"`
	TLD          string         `yaml:"tld"`
	Overrides    map[int]string `yaml:"overrides"`    // localPort → domain identifier
	ServicePorts map[string]int `yaml:"service_ports"` // serviceName → proxy listen port
}

func Default() *Config {
	svcPorts := make(map[string]int, len(defaultServicePorts))
	for k, v := range defaultServicePorts {
		svcPorts[k] = v
	}
	return &Config{
		PollInterval: 2 * time.Second,
		TLD:          "tunnel.test",
		Overrides:    make(map[int]string),
		ServicePorts: svcPorts,
	}
}

func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "local-auto-domain", "config.yaml")
}

func Load() (*Config, error) {
	cfg := Default()
	path := Path()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	return cfg, yaml.Unmarshal(data, cfg)
}

func (c *Config) Save() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Config) SetOverride(port int, name string) error {
	c.Overrides[port] = name
	return c.Save()
}

func (c *Config) RemoveOverride(port int) error {
	delete(c.Overrides, port)
	return c.Save()
}

// ServiceName returns the canonical service name for a remote port.
func ServiceName(remotePort int) string {
	if name, ok := serviceNameByPort[remotePort]; ok {
		return name
	}
	return ""
}

// ServiceProxyPort returns the configured proxy listen port for a remote port.
// Falls back to remotePort if no mapping exists.
func (c *Config) ServiceProxyPort(remotePort int) int {
	svc := ServiceName(remotePort)
	if svc == "" {
		return remotePort
	}
	if p, ok := c.ServicePorts[svc]; ok {
		return p
	}
	return remotePort
}
