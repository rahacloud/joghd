package alerter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rahacloud/joghd/internal/domain"
)

// stubAlerter records the alerts it receives and can fail on demand.
type stubAlerter struct {
	name     string
	err      error
	received atomic.Int64
}

func (s *stubAlerter) Send(_ context.Context, _ domain.Alert) error {
	s.received.Add(1)

	return s.err
}

func (s *stubAlerter) Name() string { return s.name }

func alertFor(company string) domain.Alert {
	return domain.NewFailureAlert(domain.CheckResult{
		Target: domain.Target{
			Name:           "api",
			URL:            "https://api.example.com/health",
			ExpectedStatus: 200,
			Company:        company,
			Contact:        "@oncall",
		},
		ActualStatus: 500,
		Error:        errors.New("status mismatch: expected 200, got 500"),
		Latency:      42 * time.Millisecond,
		Attempts:     3,
	})
}

func TestCompanyFilter(t *testing.T) {
	tests := []struct {
		name        string
		companies   []string
		company     string
		wantForward bool
	}{
		{name: "empty list is a catch-all", companies: nil, company: "Acme", wantForward: true},
		{name: "matching company forwards", companies: []string{"Acme"}, company: "Acme", wantForward: true},
		{name: "other company is dropped", companies: []string{"Acme"}, company: "Globex", wantForward: false},
		{name: "empty company against a list is dropped", companies: []string{"Acme"}, company: "", wantForward: false},
		{name: "one of several matches", companies: []string{"Acme", "Globex"}, company: "Globex", wantForward: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &stubAlerter{name: "inner"}
			f := NewCompanyFilter(inner, tc.companies)

			if err := f.Send(t.Context(), alertFor(tc.company)); err != nil {
				t.Fatalf("Send: %v", err)
			}

			forwarded := inner.received.Load() == 1
			if forwarded != tc.wantForward {
				t.Errorf("forwarded = %v, want %v", forwarded, tc.wantForward)
			}
		})
	}
}

func TestCompositeAlerterAggregatesErrors(t *testing.T) {
	ok := &stubAlerter{name: "ok"}
	broken := &stubAlerter{name: "broken", err: errors.New("boom")}
	alsoBroken := &stubAlerter{name: "also-broken", err: errors.New("bang")}

	c := NewCompositeAlerter(ok, broken)
	c.Add(alsoBroken)

	err := c.Send(t.Context(), alertFor("Acme"))
	if err == nil {
		t.Fatal("expected an aggregated error, got nil")
	}

	// A failing alerter must not stop the others from being tried.
	for _, a := range []*stubAlerter{ok, broken, alsoBroken} {
		if a.received.Load() != 1 {
			t.Errorf("%s received %d alerts, want 1", a.Name(), a.received.Load())
		}
	}

	for _, want := range []string{"broken: boom", "also-broken: bang"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestCompositeAlerterSucceedsWhenAllSucceed(t *testing.T) {
	c := NewCompositeAlerter(&stubAlerter{name: "a"}, &stubAlerter{name: "b"})

	if err := c.Send(t.Context(), alertFor("Acme")); err != nil {
		t.Errorf("Send: %v", err)
	}
	if name := c.Name(); !strings.Contains(name, "a") || !strings.Contains(name, "b") {
		t.Errorf("Name = %q, want it to list both alerters", name)
	}
}

func TestTelegramAlerterSuccess(t *testing.T) {
	var gotBody atomic.Value

	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody.Store(string(buf))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	a := NewTelegramAlerter("raha", "token", "-100", 5*time.Second)
	a.client.SetTransport(srv.Client().Transport).SetBaseURL(srv.URL)

	if err := a.Send(t.Context(), alertFor("Acme")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if body, _ := gotBody.Load().(string); !strings.Contains(body, "-100") {
		t.Errorf("request body %q does not carry the chat id", body)
	}
	if a.Name() != "telegram:raha" {
		t.Errorf("Name = %q, want %q", a.Name(), "telegram:raha")
	}
}

// WithProxy must reach the client the alerter sends through. The Bot API host
// is under .invalid, so the send only succeeds via the proxy.
func TestTelegramAlerterSendsThroughProxy(t *testing.T) {
	var proxiedHost atomic.Value

	proxySrv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedHost.Store(r.URL.Host)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	proxySrv.Start()

	a := NewTelegramAlerter("raha", "token", "-100", 5*time.Second, WithProxy(proxySrv.URL))
	a.client.SetBaseURL("http://api.telegram.invalid/bottoken")

	if err := a.Send(t.Context(), alertFor("Acme")); err != nil {
		t.Fatalf("Send through the proxy: %v", err)
	}
	if got, _ := proxiedHost.Load().(string); got != "api.telegram.invalid" {
		t.Errorf("proxy saw host %q, want %q", got, "api.telegram.invalid")
	}
}

// The error used to report "status 0" with an empty description because resty
// never decoded the failure response.
func TestTelegramAlerterReportsAPIErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantInText []string
	}{
		{
			name:       "structured api error",
			status:     http.StatusBadRequest,
			body:       `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`,
			wantInText: []string{"http status 400", "error_code 400", "chat not found"},
		},
		{
			name:       "ok=false with a 200 status",
			status:     http.StatusOK,
			body:       `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`,
			wantInText: []string{"http status 200", "bot was blocked by the user"},
		},
		{
			name:       "non-json gateway error falls back to the body",
			status:     http.StatusBadGateway,
			body:       "<html>502 Bad Gateway</html>",
			wantInText: []string{"http status 502", "502 Bad Gateway"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if strings.HasPrefix(tc.body, "{") {
					w.Header().Set("Content-Type", "application/json")
				} else {
					w.Header().Set("Content-Type", "text/html")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))

			a := NewTelegramAlerter("raha", "token", "-100", 5*time.Second)
			a.client.SetTransport(srv.Client().Transport).SetBaseURL(srv.URL)

			err := a.Send(t.Context(), alertFor("Acme"))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}

			for _, want := range tc.wantInText {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestMattermostAlerter(t *testing.T) {
	var gotPath atomic.Value

	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		_, _ = w.Write([]byte("ok"))
	}))

	// srv.URL is only populated once Client/Start has been called.
	transport := srv.Client().Transport

	a := NewMattermostAlerter("team", srv.URL+"/hooks/abc", "#alerts", "Joghd", "", 5*time.Second)
	a.client.SetTransport(transport)

	if err := a.Send(t.Context(), alertFor("Acme")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path, _ := gotPath.Load().(string); path != "/hooks/abc" {
		t.Errorf("webhook path = %q, want %q", path, "/hooks/abc")
	}
	if a.Name() != "mattermost:team" {
		t.Errorf("Name = %q, want %q", a.Name(), "mattermost:team")
	}
}

func TestMattermostAlerterReportsWebhookErrors(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Invalid webhook"))
	}))

	transport := srv.Client().Transport

	a := NewMattermostAlerter("team", srv.URL+"/hooks/nope", "", "", "", 5*time.Second)
	a.client.SetTransport(transport)

	err := a.Send(t.Context(), alertFor("Acme"))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, want := range []string{"status 404", "Invalid webhook"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestFormatTelegramMessage(t *testing.T) {
	failure := alertFor("Acme")

	msg := formatTelegramMessage(failure)
	for _, want := range []string{"FAILURE", "api", "https://api.example.com/health", "Acme", "@oncall", "status mismatch"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message is missing %q:\n%s", want, msg)
		}
	}

	recovery := domain.NewRecoveryAlert(domain.CheckResult{
		Target:  failure.Target,
		Success: true,
	})
	recoveryMsg := formatTelegramMessage(recovery)
	if !strings.Contains(recoveryMsg, "RECOVERED") {
		t.Errorf("recovery message is missing the header:\n%s", recoveryMsg)
	}
	if strings.Contains(recoveryMsg, "tg-spoiler") {
		t.Errorf("recovery message should not carry an error block:\n%s", recoveryMsg)
	}
}

func TestBuildMattermostAttachment(t *testing.T) {
	tests := []struct {
		name      string
		alert     domain.Alert
		wantColor string
		wantTitle string
	}{
		{name: "failure", alert: alertFor("Acme"), wantColor: mattermostColorFailure, wantTitle: "FAILURE"},
		{
			name:      "recovery",
			alert:     domain.NewRecoveryAlert(domain.CheckResult{Target: domain.Target{Name: "api"}, Success: true}),
			wantColor: mattermostColorRecovery,
			wantTitle: "RECOVERED",
		},
		{
			name:      "reminder",
			alert:     domain.NewReminderAlert(domain.CheckResult{Target: domain.Target{Name: "api"}}),
			wantColor: mattermostColorReminder,
			wantTitle: "STILL DOWN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildMattermostAttachment(tc.alert)

			if got.Color != tc.wantColor {
				t.Errorf("Color = %q, want %q", got.Color, tc.wantColor)
			}
			if !strings.Contains(got.Title, tc.wantTitle) {
				t.Errorf("Title = %q, want it to contain %q", got.Title, tc.wantTitle)
			}
		})
	}
}

func tcpFailure() domain.Alert {
	return domain.NewFailureAlert(domain.CheckResult{
		Target:   domain.Target{Name: "db", URL: "tcp://db.example.com:5432", ExpectedStatus: 200},
		Error:    errors.New("connecting to db.example.com:5432: connection refused"),
		Latency:  3 * time.Millisecond,
		Attempts: 3,
	})
}

// A TCP connect has no status codes, so "Expected 200 → Actual 0" would only
// mislead whoever is on call.
func TestAlertsDescribeTCPChecks(t *testing.T) {
	alert := tcpFailure()

	msg := formatTelegramMessage(alert)
	if !strings.Contains(msg, "TCP connect</b> to <code>db.example.com:5432</code>") {
		t.Errorf("telegram message does not describe the TCP connect:\n%s", msg)
	}
	if strings.Contains(msg, "Expected") {
		t.Errorf("telegram message shows HTTP status codes for a TCP check:\n%s", msg)
	}
	if http := formatTelegramMessage(alertFor("Acme")); !strings.Contains(http, "<b>Expected:</b> 200 → <b>Actual:</b> 500") {
		t.Errorf("the HTTP format changed:\n%s", http)
	}

	att := buildMattermostAttachment(alert)
	var titles []string
	for _, f := range att.Fields {
		titles = append(titles, f.Title)
		if f.Title == "Check" && !strings.Contains(f.Value, "db.example.com:5432") {
			t.Errorf("Check field = %q, want the dialed address", f.Value)
		}
	}
	if got := strings.Join(titles, ","); got != "URL,Check,Latency,Attempts" {
		t.Errorf("mattermost fields = %s, want URL,Check,Latency,Attempts", got)
	}
}
