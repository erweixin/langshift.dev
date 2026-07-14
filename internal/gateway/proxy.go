package gateway

import (
	"errors"
	"net"
	"net/http"
	"strings"
)

type TrustedProxy struct{ Networks []*net.IPNet }

func (proxy TrustedProxy) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		remoteHost, remotePort, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			remoteHost = request.RemoteAddr
			remotePort = "0"
		}
		remoteIP := net.ParseIP(remoteHost)
		if remoteIP == nil {
			http.Error(writer, "invalid client address", http.StatusBadRequest)
			return
		}
		trusted := false
		for _, network := range proxy.Networks {
			if network != nil && network.Contains(remoteIP) {
				trusted = true
				break
			}
		}
		if trusted {
			values := request.Header.Values("X-Forwarded-For")
			if len(values) != 1 || strings.Contains(values[0], ",") {
				http.Error(writer, "invalid forwarded client address", http.StatusBadRequest)
				return
			}
			clientIP := net.ParseIP(strings.TrimSpace(values[0]))
			if clientIP == nil || clientIP.IsUnspecified() || clientIP.IsMulticast() {
				http.Error(writer, "invalid forwarded client address", http.StatusBadRequest)
				return
			}
			request.RemoteAddr = net.JoinHostPort(clientIP.String(), remotePort)
		}
		for _, name := range untrustedNetworkHeaders {
			request.Header.Del(name)
		}
		next.ServeHTTP(writer, request)
	})
}

func ParseTrustedProxyCIDRs(values []string) ([]*net.IPNet, error) {
	networks := make([]*net.IPNet, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil || network.String() != value {
			if err == nil {
				err = errors.New("trusted proxy CIDR must be canonical")
			}
			return nil, err
		}
		if _, exists := seen[value]; exists {
			return nil, errors.New("trusted proxy CIDR is duplicated")
		}
		seen[value] = struct{}{}
		networks = append(networks, network)
	}
	return networks, nil
}
