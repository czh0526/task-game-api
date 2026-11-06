package middleware

import (
	"fmt"
	"net/http"

	"github.com/zeromicro/go-zero/rest"
)

// Local Vite dev servers. 5173 is the default dev port; Vite walks forward
// when it is taken. 4173 is `vite preview`.
const (
	viteDevPortMin = 5173
	viteDevPortMax = 5185
	vitePreview    = 4173
)

// Headers the browser preflights. The client sends JSON plus X-Api-Key or
// Authorization, and Idempotency-Key on writes. go-zero's built-in allow list
// does not include the two task-game headers.
const localDevAllowHeaders = "Accept, Content-Type, Authorization, X-Api-Key, Idempotency-Key"

// LocalDevCORS answers OPTIONS and allows the local Vite origin.
// Business routes are unchanged. Other origins still reach the handlers;
// they simply do not receive Access-Control-Allow-Origin.
func LocalDevCORS() rest.RunOption {
	return rest.WithCustomCors(setLocalDevCORSHeaders, nil, localDevOrigins()...)
}

func setLocalDevCORSHeaders(header http.Header) {
	header.Set("Access-Control-Allow-Headers", localDevAllowHeaders)
	header.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
}

func localDevOrigins() []string {
	hosts := []string{"http://localhost", "http://127.0.0.1", "http://[::1]"}
	ports := []int{vitePreview}
	for port := viteDevPortMin; port <= viteDevPortMax; port++ {
		ports = append(ports, port)
	}
	origins := make([]string, 0, len(hosts)*len(ports))
	for _, host := range hosts {
		for _, port := range ports {
			origins = append(origins, fmt.Sprintf("%s:%d", host, port))
		}
	}
	return origins
}
