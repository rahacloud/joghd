// Package proxy interprets the proxy settings shared by health checks and
// alerters.
//
// A proxy setting is one of three things:
//
//   - empty: no explicit proxy; HTTP traffic follows the standard
//     HTTP_PROXY/HTTPS_PROXY/NO_PROXY environment variables
//   - [Direct]: no proxy at all, the environment variables included
//   - a proxy URL with an http, https, socks5 or socks5h scheme
//
// [Validate] checks a setting at startup, [Apply] configures a resty client
// with it, and [Dialer] builds the dialer a TCP connect check uses. TCP checks
// cannot go through an HTTP proxy and do not read the proxy environment.
package proxy

import (
	"context"
	"fmt"
	"net"
	"net/url"

	xproxy "golang.org/x/net/proxy"
	"resty.dev/v3"
)

// Direct is the proxy setting that disables proxying entirely, ignoring the
// proxy environment variables as well.
const Direct = "direct"

// Proxy URL schemes accepted by [Validate].
const (
	SchemeHTTP    = "http"
	SchemeHTTPS   = "https"
	SchemeSOCKS5  = "socks5"
	SchemeSOCKS5H = "socks5h"
)

// Label renders a setting for logs and error messages, with any credentials
// in a proxy URL redacted.
func Label(setting string) string {
	switch setting {
	case "":
		return "environment"
	case Direct:
		return Direct
	}

	u, err := url.Parse(setting)
	if err != nil {
		return "invalid proxy url"
	}

	return u.Redacted()
}

// Validate reports whether setting is usable as a proxy setting: empty,
// [Direct], or a URL with a supported scheme and a host.
func Validate(setting string) error {
	if setting == "" || setting == Direct {
		return nil
	}

	u, err := url.Parse(setting)
	if err != nil {
		return fmt.Errorf("invalid proxy url: %w", err)
	}

	switch u.Scheme {
	case SchemeHTTP, SchemeHTTPS, SchemeSOCKS5, SchemeSOCKS5H:
	default:
		return fmt.Errorf("proxy %q must use the http, https, socks5 or socks5h scheme, or be %q", Label(setting), Direct)
	}

	if u.Host == "" {
		return fmt.Errorf("proxy %q is missing a host", Label(setting))
	}

	return nil
}

// IsSOCKS reports whether setting is a SOCKS5 proxy URL. It expects a setting
// that has already passed [Validate].
func IsSOCKS(setting string) bool {
	u, err := url.Parse(setting)
	if err != nil {
		return false
	}

	return u.Scheme == SchemeSOCKS5 || u.Scheme == SchemeSOCKS5H
}

// Apply configures client with setting. An empty setting leaves the client on
// its default, environment-driven proxy; [Direct] removes every proxy; a URL
// routes all of the client's requests through that proxy.
func Apply(client *resty.Client, setting string) {
	switch setting {
	case "":
	case Direct:
		client.RemoveProxy()
	default:
		client.SetProxy(setting)
	}
}

// ContextDialer opens connections under a context. *net.Dialer and the SOCKS5
// dialers returned by [Dialer] implement it.
type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Dialer returns the dialer for a TCP connect check under setting. An empty
// setting or [Direct] dials directly with forward; a socks5 or socks5h URL
// tunnels through that SOCKS5 proxy, which also resolves the host name. An
// HTTP proxy cannot carry a raw TCP connection and is an error.
func Dialer(setting string, forward *net.Dialer) (ContextDialer, error) {
	if setting == "" || setting == Direct {
		return forward, nil
	}

	if !IsSOCKS(setting) {
		return nil, fmt.Errorf("proxy %q is not a SOCKS5 proxy and cannot carry a TCP connection", Label(setting))
	}

	u, err := url.Parse(setting)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy url: %w", err)
	}

	d, err := xproxy.FromURL(u, forward)
	if err != nil {
		return nil, fmt.Errorf("building a dialer for %q: %w", Label(setting), err)
	}

	cd, ok := d.(ContextDialer)
	if !ok {
		return nil, fmt.Errorf("the dialer for %q does not support contexts", Label(setting))
	}

	return cd, nil
}
