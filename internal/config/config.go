package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	Listen           string `json:"listen"`
	Database         string `json:"database"`
	TLSCert          string `json:"tls_cert"`
	TLSKey           string `json:"tls_key"`
	AgentSocket      string `json:"agent_socket"`
	BypassSecretsDir string `json:"bypass_secrets_dir"`
	CookieSecure     bool   `json:"cookie_secure"`
}

func Load(path string) (Config, error) {
	c := Config{
		Listen:           ":9443",
		Database:         "/var/lib/vpn-panel/panel.db",
		TLSCert:          "/etc/vpn-panel/tls.crt",
		TLSKey:           "/etc/vpn-panel/tls.key",
		AgentSocket:      "/run/vpn-panel/agent.sock",
		BypassSecretsDir: "/var/lib/vpn-panel-agent/secrets/bypass",
		CookieSecure:     true,
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}
