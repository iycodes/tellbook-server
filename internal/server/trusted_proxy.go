package server

import (
	"net"
	"net/http"
	"strings"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

var forwardedIPHeaders = []string{
	"True-Client-IP", "CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP",
}

func trustedRealIPMiddleware(rawCIDRs []string) func(http.Handler) http.Handler {
	trusted := make([]*net.IPNet, 0, len(rawCIDRs))
	for _, raw := range rawCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err == nil {
			trusted = append(trusted, network)
		}
	}
	return func(next http.Handler) http.Handler {
		realIP := chimiddleware.RealIP(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trustedProxyPeer(r.RemoteAddr, trusted) {
				realIP.ServeHTTP(w, r)
				return
			}
			for _, header := range forwardedIPHeaders {
				r.Header.Del(header)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func trustedProxyPeer(remoteAddr string, trusted []*net.IPNet) bool {
	remoteAddr = strings.TrimSpace(remoteAddr)
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
