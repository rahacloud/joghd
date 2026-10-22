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
// [Validate] checks a setting at startup and [Apply] configures a resty
// client with it.
package proxy

import (
	"fmt"
	"net/url"

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

// Validate reports whether setting is usable as a proxy setting: empty,
// [Direct], or a URL with a supported scheme and a host.
func Validate(setting string) error {
	if setting == "" || setting == Direct {
		return nil
	}

	u, err := url.Parse(setting)
	if err != nil {
		return fmt.Errorf("invalid proxy url %q: %w", setting, err)
	}

	switch u.Scheme {
	case SchemeHTTP, SchemeHTTPS, SchemeSOCKS5, SchemeSOCKS5H:
	default:
		return fmt.Errorf("proxy %q must use the http, https, socks5 or socks5h scheme, or be %q", setting, Direct)
	}

	if u.Host == "" {
		return fmt.Errorf("proxy %q is missing a host", setting)
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
