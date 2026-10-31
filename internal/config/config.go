// Package config stores the defaults used by uploads: password and expiry.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxFreeSize is the largest anonymous transfer WeTransfer accepts.
	MaxFreeSize int64 = 3 << 30 // 3 GiB
	// MaxExpiry is the longest lifetime available without an account.
	MaxExpiry = 7 * 24 * time.Hour
	// MaxFiles is the transfer file count limit reported by the web app.
	MaxFiles = 25000

	DefaultPassword = "none"
	DefaultExpiry   = "7d"
)

type Config struct {
	Password string `json:"password"`
	Expiry   string `json:"expiry"`
}

func Default() Config {
	return Config{Password: DefaultPassword, Expiry: DefaultExpiry}
}

// Path is the config file location, e.g.
// ~/Library/Application Support/wetransfer-cli/config.json on macOS.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wetransfer-cli", "config.json"), nil
}

// Load reads the config, returning defaults when the file does not exist.
func Load() (Config, string, error) {
	path, err := Path()
	if err != nil {
		return Default(), "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), path, nil
	}
	if err != nil {
		return Default(), path, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), path, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Password == "" {
		cfg.Password = DefaultPassword
	}
	if cfg.Expiry == "" {
		cfg.Expiry = DefaultExpiry
	}
	return cfg, path, nil
}

// Save writes the config with owner-only permissions.
func Save(cfg Config) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Password returns the upload password. "none" means no password, "random"
// generates a fresh one for every upload.
func (c Config) PasswordValue() (password string, generated bool, err error) {
	switch strings.ToLower(strings.TrimSpace(c.Password)) {
	case "", "none":
		return "", false, nil
	case "random":
		p, err := RandomPassword(12)
		return p, true, err
	default:
		return c.Password, false, nil
	}
}

// ExpiryValue parses the configured expiry.
func (c Config) ExpiryValue() (time.Duration, error) {
	return ParseDuration(c.Expiry)
}

// ParseDuration accepts Go durations plus a day suffix: 12h, 1d, 7d.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty expiry")
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid expiry %q", s)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid expiry %q (use 12h, 1d, 7d)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid expiry %q", s)
	}
	return d, nil
}

// FormatDuration renders a duration the way the CLI prints it.
func FormatDuration(d time.Duration) string {
	if d >= 24*time.Hour {
		days := float64(d) / float64(24*time.Hour)
		if days == float64(int64(days)) {
			return fmt.Sprintf("%dd", int64(days))
		}
		return strconv.FormatFloat(days, 'f', 1, 64) + "d"
	}
	return d.Round(time.Minute).String()
}

// RandomPassword returns a password from an unambiguous alphabet.
func RandomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}
