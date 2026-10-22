package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "unknown mode",
			body:    "[app]\nmode = \"sometimes\"\n" + targetsBlock,
			wantErr: "invalid mode",
		},
		{
			name:    "unknown log level",
			body:    "[app]\nmode = \"oneshot\"\nlog_level = \"verbose\"\n" + targetsBlock,
			wantErr: "app.log_level",
		},
		{
			// An unbuffered semaphore would block every check forever.
			name:    "zero concurrency",
			body:    "[app]\nmode = \"oneshot\"\nconcurrency = 0\n" + targetsBlock,
			wantErr: "app.concurrency must be at least 1",
		},
		{
			name:    "negative reminder multiplier",
			body:    "[app]\nmode = \"oneshot\"\nreminder_multiplier = -1\n" + targetsBlock,
			wantErr: "app.reminder_multiplier",
		},
		{
			name:    "zero http timeout",
			body:    "[app]\nmode = \"oneshot\"\n[http]\ntimeout = \"0s\"\n" + targetsBlock,
			wantErr: "http.timeout must be positive",
		},
		{
			// Would report every target as down without sending a request.
			name:    "zero retry attempts",
			body:    minimalConfig + "\n[retry]\nmax_attempts = 0\n",
			wantErr: "retry.max_attempts must be at least 1",
		},
		{
			name:    "shrinking backoff",
			body:    minimalConfig + "\n[retry]\nmultiplier = 0.5\n",
			wantErr: "retry.multiplier",
		},
		{
			name:    "max wait below initial wait",
			body:    minimalConfig + "\n[retry]\ninitial_wait = \"10s\"\nmax_wait = \"1s\"\n",
			wantErr: "retry.max_wait",
		},
		{
			name:    "no targets",
			body:    "[app]\nmode = \"oneshot\"\n",
			wantErr: "no targets configured",
		},
		{
			name:    "target without url",
			body:    "[app]\nmode = \"oneshot\"\n[[targets]]\nname = \"api\"\n",
			wantErr: "url is required",
		},
		{
			name:    "target without name",
			body:    "[app]\nmode = \"oneshot\"\n[[targets]]\nurl = \"https://api.example.com\"\n",
			wantErr: "name is required",
		},
		{
			name:    "target url without scheme",
			body:    "[app]\nmode = \"oneshot\"\n[[targets]]\nname = \"api\"\nurl = \"api.example.com/health\"\n",
			wantErr: "must use the http, https or tcp scheme",
		},
		{
			// time.NewTicker panics on a non-positive interval.
			name:    "negative interval",
			body:    "[app]\nmode = \"oneshot\"\n[[targets]]\nname = \"api\"\nurl = \"https://api.example.com\"\ninterval = \"-5s\"\n",
			wantErr: "interval must be positive",
		},
		{
			name:    "expected status out of range",
			body:    "[app]\nmode = \"oneshot\"\n[[targets]]\nname = \"api\"\nurl = \"https://api.example.com\"\nexpected_status = 42\n",
			wantErr: "not a valid HTTP status code",
		},
		{
			name:    "duplicate target names",
			body:    "[app]\nmode = \"oneshot\"\n" + targetsBlock + targetsBlock,
			wantErr: "duplicate name",
		},
		{
			name:    "telegram alerter without token",
			body:    minimalConfig + "\n[alerters.raha]\ntype = \"telegram\"\nenabled = true\nchat_id = \"-100\"\n",
			wantErr: "bot_token is required",
		},
		{
			name:    "telegram alerter without chat id",
			body:    minimalConfig + "\n[alerters.raha]\ntype = \"telegram\"\nenabled = true\nbot_token = \"t\"\n",
			wantErr: "chat_id is required",
		},
		{
			name:    "mattermost alerter without webhook",
			body:    minimalConfig + "\n[alerters.team]\ntype = \"mattermost\"\nenabled = true\n",
			wantErr: "webhook_url is required",
		},
		{
			name:    "alerter without type",
			body:    minimalConfig + "\n[alerters.raha]\nenabled = true\n",
			wantErr: "type is required",
		},
		{
			name:    "unsupported alerter type",
			body:    minimalConfig + "\n[alerters.raha]\ntype = \"carrier-pigeon\"\nenabled = true\n",
			wantErr: "is not supported",
		},
		{
			// A non-positive timeout means "no timeout" to the HTTP client.
			name:    "negative alerter timeout",
			body:    minimalConfig + "\n[alerters.raha]\ntype = \"telegram\"\nenabled = true\nbot_token = \"t\"\nchat_id = \"-100\"\ntimeout = \"-1s\"\n",
			wantErr: "timeout must be positive",
		},
		{
			name:    "tcp target without a port",
			body:    "[app]\nmode = \"oneshot\"\n[[targets]]\nname = \"db\"\nurl = \"tcp://db.example.com\"\n",
			wantErr: "must name a port",
		},
		{
			// The proxy is inherited from http.proxy here.
			name:    "tcp target behind an http proxy",
			body:    "[app]\nmode = \"oneshot\"\n[http]\nproxy = \"http://127.0.0.1:3128\"\n[[targets]]\nname = \"db\"\nurl = \"tcp://db.example.com:5432\"\n",
			wantErr: "cannot carry a tcp:// check",
		},
		{
			name:    "unsupported http proxy scheme",
			body:    "[app]\nmode = \"oneshot\"\n[http]\nproxy = \"ftp://127.0.0.1:21\"\n" + targetsBlock,
			wantErr: "http.proxy",
		},
		{
			name:    "target proxy without a scheme",
			body:    "[app]\nmode = \"oneshot\"\n[[targets]]\nname = \"api\"\nurl = \"https://api.example.com\"\nproxy = \"127.0.0.1:1080\"\n",
			wantErr: "target[0] (api): proxy",
		},
		{
			name:    "alerter proxy without a host",
			body:    minimalConfig + "\n[alerters.raha]\ntype = \"telegram\"\nenabled = true\nbot_token = \"t\"\nchat_id = \"-100\"\nproxy = \"socks5://\"\n",
			wantErr: "alerters.raha.proxy",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// A TCP target needs no expected_status, and a SOCKS5 or direct route is fine.
func TestLoadAcceptsTCPTargets(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[app]
mode = "oneshot"

[http]
proxy = "socks5h://127.0.0.1:2081"

[[targets]]
name = "db"
url = "tcp://db.example.com:5432"
expected_status = 0

[[targets]]
name = "router"
url = "tcp://192.168.78.254:22"
proxy = "direct"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Targets[0].IsTCP() || cfg.Targets[0].TCPAddress() != "db.example.com:5432" {
		t.Errorf("target[0] = %+v, want a TCP check of db.example.com:5432", cfg.Targets[0])
	}
}

// A disabled alerter is not validated, so operators can keep a
// half-configured block around.
func TestLoadAcceptsDisabledIncompleteAlerter(t *testing.T) {
	if _, err := Load(writeConfig(t, minimalConfig+`
[alerters.raha]
type = "telegram"
enabled = false
`)); err != nil {
		t.Errorf("Load: %v", err)
	}
}
