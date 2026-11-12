package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadEnvFile(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	os.Unsetenv(EnvAPIKey)
	t.Setenv("GARMIN_TZ", "Europe/Berlin") // already set: file must not override
	p := writeFile(t, "# comment\n\nexport GARMIN_API_KEY=\"abc=def\"\nGARMIN_TZ=Asia/Tbilisi\n", 0o600)
	warn, err := LoadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" {
		t.Errorf("unexpected warning %q", warn)
	}
	if got := os.Getenv(EnvAPIKey); got != "abc=def" {
		t.Errorf("key = %q", got)
	}
	if got := os.Getenv("GARMIN_TZ"); got != "Europe/Berlin" {
		t.Errorf("existing env was overridden: %q", got)
	}
	os.Unsetenv(EnvAPIKey)
}

func TestLoadEnvFileWarnsOnLoosePermissions(t *testing.T) {
	p := writeFile(t, "GARMIN_X_TEST=1\n", 0o644)
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	warn, err := LoadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn, "chmod 600") {
		t.Errorf("warning = %q", warn)
	}
	os.Unsetenv("GARMIN_X_TEST")
}

func TestLoadEnvFileRejectsGarbage(t *testing.T) {
	if _, err := LoadEnvFile(writeFile(t, "not a pair\n", 0o600)); err == nil {
		t.Fatal("want error")
	}
	if _, err := LoadEnvFile(writeFile(t, "HTTPS_PROXY=http://evil:8080\n", 0o600)); err == nil {
		t.Fatal("want error for non-GARMIN_ key")
	}
	if _, err := LoadEnvFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestLoad(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPIKeyAlias, "alias-key")
	t.Setenv(EnvCacheDir, "off")
	t.Setenv(EnvTimeZone, "Asia/Tbilisi")
	t.Setenv(EnvRegion, "")
	t.Setenv(EnvLogLevel, "debug")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "alias-key" || cfg.CacheDir != "" || cfg.Location.String() != "Asia/Tbilisi" || cfg.Region != "global" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"no key":     {EnvAPIKey: "", EnvAPIKeyAlias: ""},
		"bad region": {EnvAPIKey: "k", EnvRegion: "eu"},
		"bad tz":     {EnvAPIKey: "k", EnvTimeZone: "Mars/Base"},
		"bad level":  {EnvAPIKey: "k", EnvLogLevel: "loud"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{EnvAPIKey, EnvAPIKeyAlias, EnvRegion, EnvTimeZone, EnvLogLevel} {
				t.Setenv(k, env[k])
			}
			if _, err := Load(); err == nil {
				t.Fatal("want error")
			}
		})
	}
}
