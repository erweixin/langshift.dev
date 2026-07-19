package api

import (
	"net"
	"net/http"
	"strings"
)

const DevelopmentWorkloadIdentityHeader = "X-Lites-Development-Workload-Identity"

func NewWorkloadAuthorizer(identities []string, allowInsecureLoopback bool) WorkloadAuthorizer {
	allowed := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		identity = strings.TrimSpace(identity)
		if identity != "" {
			allowed[identity] = struct{}{}
		}
	}
	return func(request *http.Request) (string, bool) {
		if request == nil || len(allowed) == 0 {
			return "", false
		}
		if request.TLS != nil && len(request.TLS.VerifiedChains) > 0 && len(request.TLS.PeerCertificates) > 0 {
			leaf := request.TLS.PeerCertificates[0]
			for _, uri := range leaf.URIs {
				if _, ok := allowed[uri.String()]; ok {
					return uri.String(), true
				}
			}
			for _, name := range leaf.DNSNames {
				if _, ok := allowed[name]; ok {
					return name, true
				}
			}
			return "", false
		}
		if !allowInsecureLoopback {
			return "", false
		}
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			return "", false
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", false
		}
		identity := request.Header.Get(DevelopmentWorkloadIdentityHeader)
		_, ok := allowed[identity]
		return identity, ok
	}
}
