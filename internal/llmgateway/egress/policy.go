package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

var (
	ErrInvalidEndpoint = errors.New("provider endpoint is invalid")
	ErrUnsafeEndpoint  = errors.New("provider endpoint is not public")
	ErrEndpointDrift   = errors.New("provider request escaped its bound origin")
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type NetResolver struct{ Resolver *net.Resolver }

func (resolver NetResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if resolver.Resolver == nil {
		return net.DefaultResolver.LookupNetIP(ctx, network, host)
	}
	return resolver.Resolver.LookupNetIP(ctx, network, host)
}

type Endpoint struct {
	URL        *url.URL
	Origin     string
	BoundHost  string
	DialPort   string
	RequestURL string
}

type EndpointPolicy struct {
	Resolver         Resolver
	PlatformNetworks []netip.Prefix
	AllowedPorts     map[uint16]struct{}
	// PrivateEngineeringTestHost is an intentionally narrow escape hatch for
	// the TLS model adapter used by the macOS engineering gate. A caller must
	// opt in to one exact, DNS-bound host. Loopback, link-local, metadata and
	// every other private destination remain denied.
	PrivateEngineeringTestHost string
}

func (policy EndpointPolicy) Validate(ctx context.Context, rawURL, expectedBoundHost string) (Endpoint, error) {
	if policy.Resolver == nil {
		return Endpoint{}, ErrInvalidEndpoint
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.Opaque != "" {
		return Endpoint{}, ErrInvalidEndpoint
	}
	host, err := canonicalHost(parsed.Hostname())
	if err != nil || unsafeHostname(host) {
		return Endpoint{}, ErrUnsafeEndpoint
	}
	if expectedBoundHost != "" {
		expected, expectedErr := canonicalHost(expectedBoundHost)
		if expectedErr != nil || expected != host {
			return Endpoint{}, ErrEndpointDrift
		}
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	} else {
		value, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || value == 0 {
			return Endpoint{}, ErrInvalidEndpoint
		}
		if len(policy.AllowedPorts) == 0 {
			if value != 443 {
				return Endpoint{}, ErrUnsafeEndpoint
			}
		} else if _, allowed := policy.AllowedPorts[uint16(value)]; !allowed {
			return Endpoint{}, ErrUnsafeEndpoint
		}
	}
	addresses, err := policy.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return Endpoint{}, fmt.Errorf("%w: dns resolution failed", ErrUnsafeEndpoint)
	}
	if err = policy.validateAddresses(host, addresses); err != nil {
		return Endpoint{}, err
	}
	parsed.Scheme = "https"
	parsed.Host = net.JoinHostPort(host, port)
	if port == "443" {
		parsed.Host = host
	}
	parsed.RawPath = ""
	parsed.ForceQuery = false
	parsed.RawQuery = ""
	origin := "https://" + parsed.Host
	return Endpoint{URL: parsed, Origin: origin, BoundHost: host, DialPort: port, RequestURL: parsed.String()}, nil
}

func (policy EndpointPolicy) validateAddresses(host string, addresses []netip.Addr) error {
	if policy.PrivateEngineeringTestHost == "" || host != policy.PrivateEngineeringTestHost {
		return validateAddresses(addresses, policy.PlatformNetworks)
	}
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || !address.IsGlobalUnicast() || !privateEngineeringAddress(address) {
			return ErrUnsafeEndpoint
		}
		for _, network := range policy.PlatformNetworks {
			if network.IsValid() && network.Contains(address) {
				return ErrUnsafeEndpoint
			}
		}
		seen[address] = struct{}{}
	}
	if len(seen) == 0 {
		return ErrUnsafeEndpoint
	}
	return nil
}

func privateEngineeringAddress(address netip.Addr) bool {
	for _, prefix := range engineeringPrivateNetworks {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func canonicalHost(value string) (string, error) {
	value = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(value)), ".")
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return "", ErrInvalidEndpoint
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return "", ErrInvalidEndpoint
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalidEndpoint
		}
		for _, character := range label {
			if character > unicode.MaxASCII || character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return "", ErrInvalidEndpoint
			}
		}
	}
	return value, nil
}

func unsafeHostname(host string) bool {
	for _, suffix := range []string{"localhost", ".localhost", ".local", ".internal", ".home.arpa", ".arpa", ".lan", ".home"} {
		if host == suffix || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	for _, metadataHost := range []string{"metadata.google.internal", "metadata.aws.internal", "metadata.azure.internal", "instance-data.ec2.internal"} {
		if host == metadataHost {
			return true
		}
	}
	return false
}

func validateAddresses(addresses []netip.Addr, platformNetworks []netip.Prefix) error {
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || !address.IsGlobalUnicast() || deniedAddress(address) {
			return ErrUnsafeEndpoint
		}
		for _, network := range platformNetworks {
			if network.IsValid() && network.Contains(address) {
				return ErrUnsafeEndpoint
			}
		}
		seen[address] = struct{}{}
	}
	if len(seen) == 0 {
		return ErrUnsafeEndpoint
	}
	return nil
}

func deniedAddress(address netip.Addr) bool {
	for _, prefix := range deniedNetworks {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

var deniedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var engineeringPrivateNetworks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}
