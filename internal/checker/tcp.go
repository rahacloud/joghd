package checker

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/rahacloud/joghd/internal/proxy"
)

// TCPDialer abstracts TCP connect checks for testability.
type TCPDialer interface {
	// Dial connects to address, directly or through the proxy setting, and
	// closes the connection again. It reports how long connecting took, also
	// when it failed.
	Dial(ctx context.Context, address, proxySetting string, timeout time.Duration) (time.Duration, error)
}

// NetDialer is the production [TCPDialer]. Only SOCKS5 proxies can carry a
// TCP check; configuration rejects an HTTP proxy on a tcp:// target.
type NetDialer struct{}

// Dial connects to address within timeout and closes the connection.
func (NetDialer) Dial(ctx context.Context, address, proxySetting string, timeout time.Duration) (time.Duration, error) {
	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	dialer, err := proxy.Dialer(proxySetting, &net.Dialer{})
	if err != nil {
		return 0, err
	}

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", address)
	latency := time.Since(start)

	if err != nil {
		return latency, fmt.Errorf("connecting to %s: %w", address, err)
	}

	_ = conn.Close()

	return latency, nil
}
