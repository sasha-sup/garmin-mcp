// Package config reads server settings from the environment and an optional
// .env file.
package config

import (
	"bufio"
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
)

// Config holds the resolved settings.
type Config struct {
	APIKey   string
	Region   string         // "global" or "cn"
	CacheDir string         // "" disables the disk cache
	Location *time.Location // the user's time zone; decides what "today" is
	LogLevel slog.Level
}

// Load builds a Config from the process environment.
func Load() (Config, error) {
	cfg := Config{Region: "global", Location: time.Local, LogLevel: slog.LevelInfo}

	cfg.APIKey = strings.TrimSpace(os.Getenv(EnvAPIKey))
	if cfg.APIKey == "" {
		cfg.APIKey = strings.TrimSpace(os.Getenv(EnvAPIKeyAlias))
	}
	if cfg.APIKey == "" {
		return Config{}, fmt.Errorf("%s is not set: create a key at https://connect.garmin.com/app/settings/personalApiToken "+
			"and pass it via the environment or -env-file", EnvAPIKey)
	}

	if r := strings.ToLower(strings.TrimSpace(os.Getenv(EnvRegion))); r != "" {
		if r != "global" && r != "cn" {
			return Config{}, fmt.Errorf("%s=%q: want \"global\" or \"cn\"", EnvRegion, r)
		}
		cfg.Region = r
	}

	switch dir := strings.TrimSpace(os.Getenv(EnvCacheDir)); dir {
	case "off", "none", "false", "0":
		cfg.CacheDir = ""
	case "":
		base, err := os.UserCacheDir()
		if err != nil {
			return Config{}, fmt.Errorf("find user cache dir (set %s to a path or \"off\"): %w", EnvCacheDir, err)
		}
		cfg.CacheDir = filepath.Join(base, "garmin-mcp")
	default:
		cfg.CacheDir = dir
	}

	if tz := strings.TrimSpace(os.Getenv(EnvTimeZone)); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: want an IANA zone such as Asia/Tbilisi: %w", EnvTimeZone, tz, err)
		}
		cfg.Location = loc
	}

	if lvl := strings.TrimSpace(os.Getenv(EnvLogLevel)); lvl != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			return Config{}, fmt.Errorf("%s=%q: want debug, info, warn or error", EnvLogLevel, lvl)
		}
	}
	return cfg, nil
}

// LoadEnvFile reads KEY=VALUE lines from path into the process environment.
// Variables that are already set win over the file. It returns a warning
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
		val = unquote(strings.TrimSpace(val))
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

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}
