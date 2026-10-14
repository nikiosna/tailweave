// Package ui is tailweave's optional web UI, started with `tailweave ui`. It serves one
// page and forwards the page's API calls to the daemon's Unix socket; it keeps no state.
package ui

import (
	_ "embed"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/Nikiosna/tailweave/internal/client"
)

//go:embed ui.html
var page []byte

// Handler serves the page and proxies /api/ to the daemon listening on socket.
func Handler(socket string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "tailweave"})
	proxy.Transport = client.Transport(socket)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "daemon at "+socket+": "+err.Error(), http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.Handle("/api/", proxy)
	return mux
}
