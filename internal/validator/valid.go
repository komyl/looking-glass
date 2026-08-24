package validator

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hostnameRe = regexp.MustCompile(
	`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`,
)

func ValidateTarget(s string) error {
	if s == "" {
		return fmt.Errorf("target is required")
	}
	if len(s) > 253 {
		return fmt.Errorf("target too long (max 253 chars)")
	}
	if strings.ContainsAny(s, ";&|$(){}[]<>'\"`\n\r\t\\") {
		return fmt.Errorf("invalid characters in target")
	}
	if net.ParseIP(s) != nil {
		return nil
	}
	if hostnameRe.MatchString(s) {
		return nil
	}
	return fmt.Errorf("invalid target: must be a valid IP address or hostname")
}

// ValidateNotPrivate rejects targets that are, or resolve via DNS to, a
// loopback, private (RFC 1918/4193), link-local, unspecified, or multicast
// address — including 169.254.169.254, the common cloud-metadata endpoint.
// Callers must run ValidateTarget first; s is assumed to already be a bare
// hostname or IP literal with no port.
//
// On success it also returns the resolved IP (the first address returned
// by DNS, or s itself parsed as an IP literal). Callers that connect to
// the target directly must reuse that IP for the actual network operation
// instead of re-resolving the hostname, otherwise a DNS-rebinding attacker
// can serve a public answer here and a private one moments later. A nil IP
// with a nil error means DNS resolution failed or returned no records;
// callers should fall back to the original hostname and let the downstream
// operation report the resolution failure.
func ValidateNotPrivate(ctx context.Context, s string) (net.IP, error) {
	if ip := net.ParseIP(s); ip != nil {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("target resolves to a private or reserved address")
		}
		return ip, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(lookupCtx, "ip", s)
	if err != nil || len(ips) == 0 {
		return nil, nil
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("target resolves to a private or reserved address")
		}
	}
	return ips[0], nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}

// ValidateHTTPTarget requires s to be a URL with scheme exactly "http" or
// "https" — no scheme, or any other scheme, is rejected outright. The host
// is then run through ValidateNotPrivate, same as every other target-facing
// endpoint; see that function's doc comment for the resolved-IP contract.
func ValidateHTTPTarget(ctx context.Context, s string) (net.IP, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("URL scheme must be http or https")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("URL must include a host")
	}
	return ValidateNotPrivate(ctx, u.Hostname())
}

func ValidatePrefix(s string) error {
	_, _, err := net.ParseCIDR(s)
	if err != nil {
		return fmt.Errorf("invalid CIDR prefix %q", s)
	}
	return nil
}

func ValidateASN(s string) (int, error) {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "AS")
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 4294967295 {
		return 0, fmt.Errorf("invalid ASN %q: must be 1-4294967295", s)
	}
	return n, nil
}
