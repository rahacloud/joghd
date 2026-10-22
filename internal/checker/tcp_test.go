package checker

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rahacloud/joghd/internal/domain"
)

// listen opens a TCP listener that accepts and immediately closes every
// connection, and returns its address.
func listen(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	return ln.Addr().String()
}

// closedAddress returns an address nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	addr := ln.Addr().String()
	_ = ln.Close()

	return addr
}

// socks5Server is a minimal SOCKS5 proxy (no authentication, CONNECT only)
// that sends every connection to upstream, whatever was asked for, and
// records the requested address.
func socks5Server(t *testing.T, upstream string) (string, *atomic.Value) {
	t.Helper()

	var requested atomic.Value

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				// Greeting: version, method count, methods; answer "no auth".
				head := make([]byte, 2)
				if _, err := io.ReadFull(conn, head); err != nil {
					return
				}
				if _, err := io.ReadFull(conn, make([]byte, head[1])); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 0})

				// Request: version, CONNECT, reserved, address type.
				req := make([]byte, 4)
				if _, err := io.ReadFull(conn, req); err != nil {
					return
				}

				var host string
				switch req[3] {
				case 1:
					ip := make([]byte, 4)
					_, _ = io.ReadFull(conn, ip)
					host = net.IP(ip).String()
				case 3:
					n := make([]byte, 1)
					_, _ = io.ReadFull(conn, n)
					name := make([]byte, n[0])
					_, _ = io.ReadFull(conn, name)
					host = string(name)
				default:
					return
				}

				port := make([]byte, 2)
				_, _ = io.ReadFull(conn, port)
				requested.Store(net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))))

				up, err := net.Dial("tcp", upstream)
				if err != nil {
					_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				_ = up.Close()

				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
			}()
		}
	}()

	return ln.Addr().String(), &requested
}

func TestNetDialer(t *testing.T) {
	open := listen(t)

	if _, err := (NetDialer{}).Dial(t.Context(), open, "", time.Second); err != nil {
		t.Errorf("dialing an open port: %v", err)
	}
	if _, err := (NetDialer{}).Dial(t.Context(), open, "direct", time.Second); err != nil {
		t.Errorf("dialing an open port directly: %v", err)
	}
	if _, err := (NetDialer{}).Dial(t.Context(), closedAddress(t), "", time.Second); err == nil {
		t.Error("dialing a closed port succeeded")
	}
}

// The host name goes to the proxy unresolved: .invalid cannot resolve here,
// so the dial only works if the SOCKS5 server is the one resolving it.
func TestNetDialerThroughSOCKS5(t *testing.T) {
	proxyAddr, requested := socks5Server(t, listen(t))

	if _, err := (NetDialer{}).Dial(t.Context(), "db.invalid:5432", "socks5h://"+proxyAddr, time.Second); err != nil {
		t.Fatalf("dialing through SOCKS5: %v", err)
	}
	if got, _ := requested.Load().(string); got != "db.invalid:5432" {
		t.Errorf("proxy was asked for %q, want %q", got, "db.invalid:5432")
	}
}

func TestNetDialerRejectsHTTPProxy(t *testing.T) {
	if _, err := (NetDialer{}).Dial(t.Context(), listen(t), "http://127.0.0.1:3128", time.Second); err == nil {
		t.Error("dialing through an HTTP proxy succeeded, want an error")
	}
}

// fakeDialer scripts TCP results and records the dialed address and proxy.
type fakeDialer struct {
	errs    []error
	calls   atomic.Int64
	address atomic.Value
	proxy   atomic.Value
}

func (f *fakeDialer) Dial(_ context.Context, address, proxySetting string, _ time.Duration) (time.Duration, error) {
	n := int(f.calls.Add(1))
	f.address.Store(address)
	f.proxy.Store(proxySetting)

	return time.Millisecond, f.errs[min(n-1, len(f.errs)-1)]
}

func TestCheckTCPTarget(t *testing.T) {
	httpClient := &fakeClient{responses: []response{{status: http.StatusOK}}}
	dialer := &fakeDialer{errs: []error{errors.New("connection refused"), nil}}

	c := New(WithHTTPClient(httpClient), WithTCPDialer(dialer), WithRetryConfig(fastRetry(3)))

	result := c.Check(t.Context(), domain.Target{
		Name:     "db",
		URL:      "tcp://db.example.com:5432",
		Timeout:  time.Second,
		Interval: time.Second,
		Proxy:    "socks5h://127.0.0.1:2081",
	})

	if !result.Success || result.Error != nil {
		t.Fatalf("result = %+v, want success after a retry", result)
	}
	if result.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", result.Attempts)
	}
	if httpClient.calls != 0 {
		t.Errorf("a TCP target made %d HTTP requests", httpClient.calls)
	}
	if got, _ := dialer.address.Load().(string); got != "db.example.com:5432" {
		t.Errorf("dialed %q, want %q", got, "db.example.com:5432")
	}
	if got, _ := dialer.proxy.Load().(string); got != "socks5h://127.0.0.1:2081" {
		t.Errorf("dialed through %q, want the target's proxy", got)
	}
}
