package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const minimalConfig = `
[app]
mode = "oneshot"

[[targets]]
name = "api"
url = "https://api.example.com/health"
`

const targetsBlock = `
[[targets]]
name = "api"
url = "https://api.example.com/health"
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	return path
}

// The env provider hands TransformFunc the raw variable name, so the prefix
// has to be stripped or every key lands under "joghd_" and overrides nothing.
func TestLoadEnvOverrides(t *testing.T) {
	path := writeConfig(t, minimalConfig+`
[alerters.team_chat]
type = "telegram"
enabled = true
bot_token = "from-file"
chat_id = "-100"
`)

	t.Setenv("JOGHD_APP__MODE", ModeContinuous)
	t.Setenv("JOGHD_APP__CONCURRENCY", "42")
	t.Setenv("JOGHD_HTTP__TIMEOUT", "3s")
	t.Setenv("JOGHD_ALERTERS__TEAM_CHAT__BOT_TOKEN", "from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.App.Mode != ModeContinuous {
		t.Errorf("app.mode = %q, want %q", cfg.App.Mode, ModeContinuous)
	}
	if cfg.App.Concurrency != 42 {
		t.Errorf("app.concurrency = %d, want 42 (string values must decode)", cfg.App.Concurrency)
	}
	if cfg.HTTP.Timeout != 3*time.Second {
		t.Errorf("http.timeout = %s, want 3s (durations must decode)", cfg.HTTP.Timeout)
	}
	if got := cfg.Alerters["team_chat"].BotToken; got != "from-env" {
		t.Errorf("bot_token = %q, want %q (env must win over the file)", got, "from-env")
	}
}

func TestLoadFileValuesWinOverDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[app]
mode = "continuous"
concurrency = 3

[[targets]]
name = "api"
url = "https://api.example.com/health"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.App.Mode != ModeContinuous {
		t.Errorf("app.mode = %q, want %q", cfg.App.Mode, ModeContinuous)
	}
	if cfg.App.Concurrency != 3 {
		t.Errorf("app.concurrency = %d, want 3", cfg.App.Concurrency)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Error("expected an error for a missing config file, got nil")
	}
}

// One http.proxy has to cover both the checks and the alerts, while a target
// or an alerter can still take its own route.
func TestLoadProxyInheritance(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[app]
mode = "oneshot"

[http]
proxy = "socks5h://127.0.0.1:2081"

[alerters.inherits]
type = "telegram"
enabled = true
bot_token = "t"
chat_id = "-100"

[alerters.own]
type = "telegram"
enabled = true
bot_token = "t"
chat_id = "-100"
proxy = "direct"

[[targets]]
name = "inherits"
url = "https://api.example.com/health"

[[targets]]
name = "own"
url = "https://intranet.example.com/health"
proxy = "http://127.0.0.1:3128"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := map[string]string{
		"alerter inherits": "socks5h://127.0.0.1:2081",
		"alerter own":      "direct",
		"target inherits":  "socks5h://127.0.0.1:2081",
		"target own":       "http://127.0.0.1:3128",
	}
	got := map[string]string{
		"alerter inherits": cfg.Alerters["inherits"].Proxy,
		"alerter own":      cfg.Alerters["own"].Proxy,
		"target inherits":  cfg.Targets[0].Proxy,
		"target own":       cfg.Targets[1].Proxy,
	}

	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s proxy = %q, want %q", k, got[k], w)
		}
	}
}

// Zero inherits http.cert_expiry_warning; a negative target value turns the
// check off for that target and survives defaulting.
func TestLoadCertExpiryWarningInheritance(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[app]
mode = "oneshot"

[http]
cert_expiry_warning = "336h"

[[targets]]
name = "inherits"
url = "https://api.example.com/health"

[[targets]]
name = "own"
url = "https://short.example.com/health"
cert_expiry_warning = "72h"

[[targets]]
name = "off"
url = "https://selfsigned.example.com/health"
cert_expiry_warning = "-1s"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for i, want := range []time.Duration{336 * time.Hour, 72 * time.Hour, -time.Second} {
		if got := cfg.Targets[i].CertExpiryWarning; got != want {
			t.Errorf("target %s cert_expiry_warning = %s, want %s", cfg.Targets[i].Name, got, want)
		}
	}
}
