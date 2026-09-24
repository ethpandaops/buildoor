package epbs

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"unicode"
)

// defaultAuthData derives the default BuilderRequestAuth data for a builder URL
// as defined by the Gloas builder-specs: the lowercased ASCII hostname, with an
// IPv6 literal inside brackets in its compressed form using hexadecimal groups
// only. Scheme, userinfo, port, path, query and fragment are not part of the
// builder's identity.
func defaultAuthData(builderURL string) ([]byte, error) {
	u, err := url.Parse(builderURL)
	if err != nil {
		return nil, fmt.Errorf("invalid builder url: %w", err)
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, errors.New("builder url has no hostname")
	}

	// Internationalized hostnames must be configured in their punycode form.
	for _, r := range host {
		if r > unicode.MaxASCII {
			return nil, errors.New("builder url hostname is not ASCII")
		}
	}

	if strings.Contains(host, ":") {
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return nil, fmt.Errorf("invalid IPv6 literal in builder url: %w", err)
		}

		host = "[" + compressIPv6(addr.WithZone("")) + "]"
	}

	return []byte(host), nil
}

// compressIPv6 formats an IPv6 address in its compressed form with hexadecimal
// groups only. netip renders IPv4-mapped addresses in the mixed dotted notation,
// which the builder-specs rule out.
func compressIPv6(addr netip.Addr) string {
	if !addr.Is4In6() {
		return addr.String()
	}

	b := addr.As16()

	return fmt.Sprintf("::ffff:%x:%x", uint16(b[12])<<8|uint16(b[13]), uint16(b[14])<<8|uint16(b[15]))
}

// matchesAuthData reports whether data authenticates a request for the builder
// reachable at builderURL. It accepts the builder-specs default derived from the
// URL and, for validator clients that still sign them, the URL bytes verbatim.
func matchesAuthData(data []byte, builderURL string) bool {
	if string(data) == builderURL {
		return true
	}

	expected, err := defaultAuthData(builderURL)

	return err == nil && bytes.Equal(data, expected)
}
