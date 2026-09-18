package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Opt-in local QA only: corrupt the first successful note and booking-command
// responses AFTER the real handler has committed. No command is fabricated.
func interruptBrowserResponses(next http.Handler) http.Handler {
	var mu sync.Mutex
	dropped := map[string]bool{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := ""
		if r.Method == http.MethodPost {
			for _, suffix := range []string{"/notes", "/commands"} {
				if strings.HasSuffix(r.URL.Path, suffix) {
					kind = suffix
				}
			}
		}
		if kind == "" {
			next.ServeHTTP(w, r)
			return
		}
		response := httptest.NewRecorder()
		next.ServeHTTP(response, r)
		mu.Lock()
		interrupt := !dropped[kind] && response.Code >= 200 && response.Code < 300
		if interrupt {
			dropped[kind] = true
		}
		mu.Unlock()
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		if interrupt {
			w.Header().Del("Content-Length")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"interrupted":`))
			return
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})
}
