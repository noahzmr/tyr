// Package settings finds Tyr's configuration files: /etc/tyr for root (and
// the live ISO), ~/.config/tyr for a user who has one there or cannot
// write /etc/tyr.
package settings

import (
	"os"
	"path/filepath"
)

// SystemDir holds the configuration for root and the live ISO.
const SystemDir = "/etc/tyr"

// File returns where the named file is read from and saved to.
func File(name string) string {
	if os.Geteuid() != 0 {
		if u := UserDir(); u != "" {
			p := filepath.Join(u, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
			if !Writable(SystemDir) {
				if _, err := os.Stat(filepath.Join(SystemDir, name)); err != nil {
					return p // nothing system-wide to read: save to the user's
				}
			}
		}
	}
	return filepath.Join(SystemDir, name)
}

// UserDir is ~/.config/tyr (or $XDG_CONFIG_HOME/tyr).
func UserDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "tyr")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "tyr")
	}
	return ""
}

// Writable reports whether files can be created in dir.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".tyr-w-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}
