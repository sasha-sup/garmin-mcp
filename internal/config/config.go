// Package config reads server settings from the environment and an optional
// .env file.
package config

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvAPIKey      = "GARMIN_API_KEY" //nolint:gosec // variable name, not a credential
	EnvAPIKeyAlias = "GARMIN_API"     // accepted for convenience
	EnvRegion      = "GARMIN_REGION"
	EnvCacheDir    = "GARMIN_CACHE_DIR"
	EnvTimeZone    = "GARMIN_TZ"
	EnvLogLevel    = "GARMIN_LOG_LEVEL"
	EnvSensitive   = "GARMIN_ALLOW_SENSITIVE"
)

// Config holds the resolved settings.
type Config struct {
	APIKey   string
	Region   string         // "global" or "cn"
	CacheDir string         // "" disables the disk cache
	Location *time.Location // the user's time zone; decides what "today" is
	LogLevel slog.Level
	// AllowSensitive enables reproductive-health and nutrition endpoints.
	AllowSensitive bool
}

// Load builds a Config from the process environment.
func Load() (Config, error) {
	cfg := Config{Region: "global", Location: time.Local, LogLevel: slog.LevelInfo}
	for _, load := range []func(*Config) error{loadAPIKey, loadRegion, loadCacheDir, loadLocation, loadSensitive, loadLogLevel} {
		if err := load(&cfg); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func env(name string) string { return strings.TrimSpace(os.Getenv(name)) }

func loadAPIKey(cfg *Config) error {
	// parseValue drops quotes that `docker run --env-file` passes through
	// literally; a real key never contains them.
	// A missing key is not an error here: the server still starts and lists
	// its tools (catalogs check that), and every call explains how to set it.
	cfg.APIKey = parseValue(cmp.Or(env(EnvAPIKey), env(EnvAPIKeyAlias)))
	return nil
}

func loadRegion(cfg *Config) error {
	switch r := strings.ToLower(env(EnvRegion)); r {
	case "":
	case "global", "cn":
		cfg.Region = r
	default:
		return fmt.Errorf("%s=%q: want \"global\" or \"cn\"", EnvRegion, r)
	}
	return nil
}

func loadCacheDir(cfg *Config) error {
	switch dir := env(EnvCacheDir); dir {
	case "off", "none", "false", "0":
		cfg.CacheDir = ""
	case "":
		base, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("find user cache dir (set %s to a path or \"off\"): %w", EnvCacheDir, err)
		}
		cfg.CacheDir = filepath.Join(base, "garmin-mcp")
	default:
		cfg.CacheDir = dir
	}
	return nil
}

func loadLocation(cfg *Config) error {
	tz := env(EnvTimeZone)
	if tz == "" {
		return nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return fmt.Errorf("%s=%q: want an IANA zone such as Europe/Berlin: %w", EnvTimeZone, tz, err)
	}
	cfg.Location = loc
	return nil
}

func loadSensitive(cfg *Config) error {
	switch v := strings.ToLower(env(EnvSensitive)); v {
	case "", "0", "false", "no":
	case "1", "true", "yes":
		cfg.AllowSensitive = true
	default:
		return fmt.Errorf("%s=%q: want 1 or 0", EnvSensitive, v)
	}
	return nil
}

func loadLogLevel(cfg *Config) error {
	if lvl := env(EnvLogLevel); lvl != "" && cfg.LogLevel.UnmarshalText([]byte(lvl)) != nil {
		return fmt.Errorf("%s=%q: want debug, info, warn or error", EnvLogLevel, lvl)
	}
	return nil
}

// LoadEnvFile reads GARMIN_* KEY=VALUE lines from path into the process
// environment. Variables that are already set win over the file. Other keys
// are rejected: a tampered file must not be able to set HTTPS_PROXY,
// SSL_CERT_FILE or GODEBUG and intercept the API key. It returns a warning
// (not an error) when the file is readable by other users.
func LoadEnvFile(path string) (warning string, err error) {
	f, err := os.Open(path) //nolint:gosec // reading a user-chosen env file is the purpose of this function
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("env file %s does not exist", path)
		}
		return "", fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()

	if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
		warning = fmt.Sprintf("env file %s has mode %v and is readable by other users; run: chmod 600 %s",
			path, st.Mode().Perm(), path)
	}

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return warning, fmt.Errorf("%s:%d: want KEY=VALUE", path, n)
		}
		if !strings.HasPrefix(key, "GARMIN_") {
			return warning, fmt.Errorf("%s:%d: only GARMIN_* variables are allowed in the env file, got %q; set others in the shell", path, n, key)
		}
		val = parseValue(strings.TrimSpace(val))
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return warning, fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return warning, fmt.Errorf("read env file: %w", err)
	}
	return warning, nil
}

// parseValue unquotes a value and drops a trailing " # comment". Inside
// quotes everything is kept literally.
func parseValue(v string) string {
	if len(v) >= 1 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : end+1]
		}
		return v
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	} else if i := strings.Index(v, "\t#"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
