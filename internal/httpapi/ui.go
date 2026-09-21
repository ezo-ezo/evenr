package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var webFiles embed.FS

// contentSecurityPolicy lets the page load only its own script and stylesheet
// and talk only to this server, so nothing from a response can run as code.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// routeUI serves the demo page at "/" and its assets under /static/.
func (h *handler) routeUI(mux *http.ServeMux) {
	site, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	static := http.FileServerFS(site)

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		setPageHeaders(w)
		r.URL.Path = "/" // the file server serves index.html for a directory; asking for it by name redirects
		static.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /static/{file}", func(w http.ResponseWriter, r *http.Request) {
		setPageHeaders(w)
		r.URL.Path = "/" + r.PathValue("file")
		static.ServeHTTP(w, r)
	})
}

func setPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// routeFeatures lets the page discover, without provoking an error, whether
// the fault-injection controls exist on this server.
func (h *handler) routeFeatures(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/features", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"admin": h.faults != nil})
	})
}
