package checker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rahacloud/joghd/internal/config"
)

// jsonServer serves a JSON body and counts how many TCP connections it accepts.
// JSON matters: resty only drains a body it can decode somewhere, so a client
// that ignores the body leaks the connection for exactly this content type.
func jsonServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var conns atomic.Int64

	// NewTestServer hands back a server that is configurable until first use
	// and shuts itself down via t.Cleanup, so there is no Close to forget.
	// Start opts into the loopback network, which these tests need because
	// NewRestyClient dials with its own transport.
	srv := httptest.NewTestServer(t, handler)
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()

	return srv, &conns
}

// inMemoryServer wires a RestyClient to a server on httptest's in-memory
// network. No sockets and no real timers are involved, which is what lets the
// caller run inside a synctest bubble.
func inMemoryServer(t *testing.T, cfg config.HTTPConfig, handler http.HandlerFunc) (*httptest.Server, *RestyClient) {
	t.Helper()

	srv := httptest.NewTestServer(t, handler)

	client := NewRestyClient(cfg)
	client.clientFor("").SetTransport(srv.Client().Transport)
	t.Cleanup(func() { _ = client.Close() })

	return srv, client
}

func okJSONHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// Every check used to build its own client and leave the response body open,
// leaking one socket per check until the process ran out of descriptors.
func TestRestyClientReusesConnections(t *testing.T) {
	srv, conns := jsonServer(t, okJSONHandler)

	client := NewRestyClient(config.HTTPConfig{Timeout: 5 * time.Second, UserAgent: "Joghd/test"})
	t.Cleanup(func() { _ = client.Close() })

	const checks = 20
	for range checks {
		resp, err := client.Execute(t.Context(), Request{Method: http.MethodGet, URL: srv.URL, Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
	}

	if got := conns.Load(); got != 1 {
		t.Errorf("%d checks opened %d connections, want 1 (response bodies are not being released)", checks, got)
	}
}

// The per-request timeout must not cost the client its TLS configuration.
func TestRestyClientSkipTLSVerification(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(okJSONHandler))
	srv.StartTLS()

	client := NewRestyClient(config.HTTPConfig{
		Timeout:             5 * time.Second,
		UserAgent:           "Joghd/test",
		SkipTLSVerification: true,
	})
	t.Cleanup(func() { _ = client.Close() })

	// A per-target timeout is always set in practice, so this is the path
	// production takes.
	resp, err := client.Execute(t.Context(), Request{Method: http.MethodGet, URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("with per-target timeout: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if _, err := client.Execute(t.Context(), Request{Method: http.MethodGet, URL: srv.URL}); err != nil {
		t.Errorf("without per-target timeout: %v", err)
	}
}

// On a fake clock the per-request timeout can be asserted to the nanosecond
// instead of being bounded by a threshold picked to survive a slow CI runner.
func TestRestyClientPerRequestTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, client := inMemoryServer(t, config.HTTPConfig{Timeout: time.Minute, UserAgent: "Joghd/test"},
			func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(2 * time.Second):
					okJSONHandler(w, r)
				case <-r.Context().Done():
				}
			})

		start := time.Now()
		if _, err := client.Execute(t.Context(), Request{Method: http.MethodGet, URL: srv.URL, Timeout: 100 * time.Millisecond}); err == nil {
			t.Fatal("expected a timeout error, got nil")
		}

		const want = 100 * time.Millisecond
		if elapsed := time.Since(start); elapsed != want {
			t.Errorf("took %s, want exactly %s; the per-request timeout was not applied", elapsed, want)
		}
	})
}

func TestRestyClientSendsHeaders(t *testing.T) {
	var gotUA, gotAuth string

	srv, _ := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		okJSONHandler(w, r)
	})

	client := NewRestyClient(config.HTTPConfig{Timeout: 5 * time.Second, UserAgent: "Joghd/test"})
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.Execute(t.Context(), Request{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer token"},
		Timeout: 5 * time.Second,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if gotUA != "Joghd/test" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "Joghd/test")
	}
	if gotAuth != "Bearer token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer token")
	}
}

func TestRestyClientContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, client := inMemoryServer(t, config.HTTPConfig{Timeout: time.Minute, UserAgent: "Joghd/test"},
			func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			})

		ctx, cancel := context.WithCancel(t.Context())

		errc := make(chan error, 1)
		go func() {
			_, err := client.Execute(ctx, Request{Method: http.MethodGet, URL: srv.URL, Timeout: time.Minute})
			errc <- err
		}()

		// Let the request reach the handler and park there, then cancel.
		synctest.Sleep(50 * time.Millisecond)
		cancel()

		if err := <-errc; err == nil {
			t.Error("expected an error after cancellation, got nil")
		}
	})
}

// forwardProxy is a minimal HTTP forward proxy: it answers every request it
// receives for an absolute URL itself and records the hosts it was asked for.
// Targets under the .invalid TLD cannot resolve, so a request only succeeds
// if it really went through the proxy.
func forwardProxy(t *testing.T) (*httptest.Server, *atomic.Value) {
	t.Helper()

	var host atomic.Value

	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}

		host.Store(r.URL.Host)
		okJSONHandler(w, r)
	}))
	srv.Start()

	return srv, &host
}

func TestRestyClientRoutesThroughPerRequestProxy(t *testing.T) {
	proxySrv, host := forwardProxy(t)

	client := NewRestyClient(config.HTTPConfig{Timeout: 5 * time.Second, UserAgent: "Joghd/test"})
	t.Cleanup(func() { _ = client.Close() })

	resp, err := client.Execute(t.Context(), Request{
		Method:  http.MethodGet,
		URL:     "http://target.invalid/health",
		Timeout: 5 * time.Second,
		Proxy:   proxySrv.URL,
	})
	if err != nil {
		t.Fatalf("Execute through the proxy: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got, _ := host.Load().(string); got != "target.invalid" {
		t.Errorf("proxy saw host %q, want %q", got, "target.invalid")
	}
}

// Targets that share a proxy setting must share a client and its connection
// pool; a different setting gets its own client, and Close reaches them all.
func TestRestyClientOneClientPerProxySetting(t *testing.T) {
	client := NewRestyClient(config.HTTPConfig{Timeout: 5 * time.Second, UserAgent: "Joghd/test"})

	a := client.clientFor("socks5h://127.0.0.1:1080")
	if b := client.clientFor("socks5h://127.0.0.1:1080"); a != b {
		t.Error("the same proxy setting built two clients")
	}
	if c := client.clientFor("direct"); a == c {
		t.Error("different proxy settings share one client")
	}
	if c := client.clientFor("direct"); c.IsProxySet() {
		t.Error("the direct client has a proxy set")
	}

	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
