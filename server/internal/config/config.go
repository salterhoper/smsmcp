// Package config resolves runtime settings from flags and environment.
package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataDir   string // holds smsmcp.db
	Addr      string // listen address, e.g. ":8080"
	PublicURL string // URL phones and agents use to reach this server
}

// Default fills settings from SMSMCP_* environment variables with sane fallbacks.
func Default() Config {
	c := Config{
		DataDir:   os.Getenv("SMSMCP_DATA"),
		Addr:      envOr("SMSMCP_ADDR", ":8080"),
		PublicURL: os.Getenv("SMSMCP_PUBLIC_URL"),
	}
	if c.DataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			c.DataDir = filepath.Join(home, ".smsmcp")
		} else {
			c.DataDir = "./data"
		}
	}
	return c
}

func (c Config) DBPath() string { return filepath.Join(c.DataDir, "smsmcp.db") }

// ResolvedPublicURL returns PublicURL, or http://<LAN IP><port> when unset.
func (c Config) ResolvedPublicURL() string {
	if c.PublicURL != "" {
		return strings.TrimRight(c.PublicURL, "/")
	}
	port := c.Addr
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i:]
	}
	return "http://" + lanIP() + port
}

func lanIP() string {
	// No packets are sent; this only asks the kernel which interface routes out.
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
