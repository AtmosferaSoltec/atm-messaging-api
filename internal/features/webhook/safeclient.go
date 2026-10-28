package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

const maxURLLength = 2048

// ErrBlockedAddress is returned when a webhook URL resolves to a private or internal address.
var ErrBlockedAddress = errors.New("webhook target resolves to a blocked address")

var carrierGradeNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// ValidateURL checks a webhook URL's shape. Plain http is only accepted when
// allowInsecure is set (development).
func ValidateURL(raw string, allowInsecure bool) error {
	if len(raw) > maxURLLength {
		return errors.New("url is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("url must be absolute, e.g. https://example.com/hooks")
	}
	if parsed.User != nil {
		return errors.New("url must not contain credentials")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !allowInsecure {
			return errors.New("url must use https")
		}
	default:
		return errors.New("url must use https")
	}
	return nil
}

// NewHTTPClient returns the client that delivers webhooks. Unless allowPrivate
// is set it refuses to connect to loopback, private, link-local and similar
// addresses, checked on the address actually dialed (so DNS tricks do not help),
// and it never follows redirects.
func NewHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrBlockedAddress, err)
			}
			if isBlocked(net.ParseIP(host)) {
				return ErrBlockedAddress
			}
			return nil
		}
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil, // a proxy would bypass the address check
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func isBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || carrierGradeNAT.Contains(ip)
}
