package main

import (
	"net/url"
	"strings"
)

// Browser help for app development: local dev URLs get http://.

// fixURL adds a scheme: http:// for this PC's dev servers, https:// otherwise.
func fixURL(u string) string {
	if strings.Contains(u, "://") || strings.HasPrefix(u, "about:") || strings.HasPrefix(u, "file:") {
		return u
	}
	host := strings.ToLower(u)
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if p, err := url.Parse("http://" + host); err == nil {
		h := p.Hostname()
		if h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "127.0.0.1" || h == "::1" || h == "0.0.0.0" || strings.HasPrefix(h, "192.168.") || strings.HasPrefix(h, "10.") {
			return "http://" + u
		}
	}
	return "https://" + u
}


