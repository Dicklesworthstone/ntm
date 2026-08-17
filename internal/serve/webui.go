package serve

// Static web UI mounting (F1b, bd-ws5-ship-or-cut-jv0rc.1.2).
//
// When Config.WebUI is set (the go:embed static export from internal/webui),
// the router's NotFound handler serves it: every path that is not an API
// route falls through to the exported Next.js site. The UI therefore runs
// behind the same middleware chain (auth, CORS, redaction) as the API.

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// apiPrefixes are namespaces that must keep their JSON 404 semantics even
// when the web UI is mounted; everything else falls through to static files.
var apiReservedPrefixes = []string{"/api", "/events", "/health", "/docs", "/ws"}

func isReservedAPIPath(p string) bool {
	for _, prefix := range apiReservedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// webUIHandler serves the embedded static export. Directory-style routes
// (trailingSlash export) resolve to <route>/index.html; unknown paths get
// the exported 404 page with a 404 status.
func (s *Server) webUIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestIDFromContext(r.Context())

		if isReservedAPIPath(r.URL.Path) {
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "route not found", nil, reqID)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErrorResponse(w, http.StatusMethodNotAllowed, ErrCodeBadRequest, "method not allowed", nil, reqID)
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")

		candidates := []string{}
		switch {
		case name == "":
			candidates = append(candidates, "index.html")
		case strings.HasSuffix(r.URL.Path, "/"):
			candidates = append(candidates, name+"/index.html")
		default:
			candidates = append(candidates, name, name+"/index.html", name+".html")
		}

		for _, candidate := range candidates {
			if s.serveWebUIFile(w, candidate, http.StatusOK) {
				return
			}
		}

		// Unknown UI path: serve the exported 404 page with a 404 status.
		if s.serveWebUIFile(w, "404.html", http.StatusNotFound) {
			return
		}
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "not found", nil, reqID)
	}
}

// serveWebUIFile writes the named embedded file if it exists as a regular
// file, returning true on success.
func (s *Server) serveWebUIFile(w http.ResponseWriter, name string, status int) bool {
	if s.webUI == nil {
		return false
	}
	info, err := fs.Stat(s.webUI, name)
	if err != nil || info.IsDir() {
		return false
	}
	data, err := fs.ReadFile(s.webUI, name)
	if err != nil {
		return false
	}

	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", contentType)
	if strings.HasPrefix(name, "_next/static/") {
		// Hash-named immutable assets.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(status)
	_, _ = w.Write(data)
	return true
}
