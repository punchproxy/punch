// Package web serves the punchd dashboard: a small single-page application
// embedded into the daemon binary and served from the same listener as the
// HTTP API. The dashboard uses hash-based routing, so the server only ever
// serves the real static assets under static/ — no SPA fallback is needed.
//
// The dashboard assets are intentionally unauthenticated: they contain no
// secrets and must load so the user can enter an API token. All privileged
// data flows through the /api endpoints, which enforce the bearer token when
// api.secret is set.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:static
var staticFS embed.FS

// Handler returns an http.Handler that serves the embedded dashboard. Mount it
// at the router root; more specific routes (e.g. /api) take precedence.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// staticFS is embedded at build time, so this can only fail if the
		// package is misconfigured — fail loudly rather than serve nothing.
		panic("web: embedded static assets missing: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets are unfingerprinted and are replaced whenever the daemon
		// binary is upgraded, so force revalidation rather than risk a client
		// pinning a stale bundle. Serving from the embedded FS is cheap.
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}
