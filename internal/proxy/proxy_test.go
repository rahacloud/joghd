package proxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"resty.dev/v3"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		wantErr bool
	}{
		{name: "empty follows the environment", setting: ""},
		{name: "direct", setting: Direct},
		{name: "http", setting: "http://127.0.0.1:8080"},
		{name: "https", setting: "https://proxy.example.com:443"},
		{name: "socks5", setting: "socks5://127.0.0.1:1080"},
		{name: "socks5h with credentials", setting: "socks5h://user:pass@127.0.0.1:1080"},
		{name: "unsupported scheme", setting: "ftp://127.0.0.1:21", wantErr: true},
		{name: "bare host", setting: "127.0.0.1:1080", wantErr: true},
		{name: "missing host", setting: "socks5://", wantErr: true},
		{name: "unparsable", setting: "http://[::1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.setting)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.setting, err, tt.wantErr)
			}
		})
	}
}

func TestIsSOCKS(t *testing.T) {
	for setting, want := range map[string]bool{
		"socks5://127.0.0.1:1080":  true,
		"socks5h://127.0.0.1:1080": true,
		"http://127.0.0.1:8080":    false,
		Direct:                     false,
		"":                         false,
	} {
		if got := IsSOCKS(setting); got != want {
			t.Errorf("IsSOCKS(%q) = %v, want %v", setting, got, want)
		}
	}
}

// proxyFor reports which proxy the client's transport would use for a request.
func proxyFor(t *testing.T, client *resty.Client) *url.URL {
	t.Helper()

	transport, err := client.HTTPTransport()
	if err != nil {
		t.Fatalf("HTTPTransport: %v", err)
	}

	if transport.Proxy == nil {
		return nil
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	u, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}

	return u
}

func TestApply(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example:3128")

	t.Run("url", func(t *testing.T) {
		client := resty.New()
		t.Cleanup(func() { _ = client.Close() })

		Apply(client, "socks5h://127.0.0.1:2081")

		if got := proxyFor(t, client); got == nil || got.String() != "socks5h://127.0.0.1:2081" {
			t.Errorf("proxy = %v, want socks5h://127.0.0.1:2081", got)
		}
	})

	t.Run("direct ignores the environment", func(t *testing.T) {
		client := resty.New()
		t.Cleanup(func() { _ = client.Close() })

		Apply(client, Direct)

		if got := proxyFor(t, client); got != nil {
			t.Errorf("proxy = %v, want none", got)
		}
	})
}

// Proxy settings reach logs and alert error text, so a password in a proxy
// URL must never be printed.
func TestLabelRedactsCredentials(t *testing.T) {
	for setting, want := range map[string]string{
		"":                                 "environment",
		Direct:                             Direct,
		"socks5h://127.0.0.1:2081":         "socks5h://127.0.0.1:2081",
		"http://user:secret@proxy.example": "http://user:xxxxx@proxy.example",
	} {
		if got := Label(setting); got != want {
			t.Errorf("Label(%q) = %q, want %q", setting, got, want)
		}
	}

	if err := Validate("ftp://user:secret@proxy.example"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("Validate error = %v, want one without the password", err)
	}
}
