package validator

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
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
