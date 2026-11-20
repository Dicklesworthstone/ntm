// Package serve provides HTTP server functionality including OpenAPI spec generation.
package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OpenAPISpec represents an OpenAPI 3.1 specification.
type OpenAPISpec struct {
	OpenAPI    string              `json:"openapi"`
	Info       OpenAPIInfo         `json:"info"`
	Servers    []OpenAPIServer     `json:"servers,omitempty"`
	Paths      map[string]PathItem `json:"paths"`
	Components *OpenAPIComponents  `json:"components,omitempty"`
	Tags       []OpenAPITag        `json:"tags,omitempty"`
}

// OpenAPIInfo contains API metadata.
type OpenAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// OpenAPIServer describes an API server.
type OpenAPIServer struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// OpenAPITag categorizes operations.
type OpenAPITag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PathItem contains operations for a path.
type PathItem struct {
	Get    *Operation `json:"get,omitempty"`
	Post   *Operation `json:"post,omitempty"`
	Put    *Operation `json:"put,omitempty"`
	Patch  *Operation `json:"patch,omitempty"`
	Delete *Operation `json:"delete,omitempty"`
}

// Operation describes a single API operation.
type Operation struct {
	Tags        []string              `json:"tags,omitempty"`
	Summary     string                `json:"summary,omitempty"`
	Description string                `json:"description,omitempty"`
	OperationID string                `json:"operationId,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"requestBody,omitempty"`
	Responses   map[string]Response   `json:"responses"`
	Security    []map[string][]string `json:"security,omitempty"`
	Deprecated  bool                  `json:"deprecated,omitempty"`
}

// Parameter describes an operation parameter.
type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
}

// RequestBody describes a request body.
type RequestBody struct {
	Description string               `json:"description,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Content     map[string]MediaType `json:"content"`
}

// Response describes an operation response.
type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// MediaType describes media type content.
type MediaType struct {
	Schema   *Schema               `json:"schema,omitempty"`
	Examples map[string]ExampleRef `json:"examples,omitempty"`
}

// ExampleRef holds an example value.
type ExampleRef struct {
	Summary string `json:"summary,omitempty"`
	Value   any    `json:"value,omitempty"`
}

// Schema describes a JSON Schema.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
}

// OpenAPIComponents holds reusable components.
type OpenAPIComponents struct {
	Schemas         map[string]*Schema         `json:"schemas,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
}

// SecurityScheme describes an authentication scheme.
type SecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
	Name         string `json:"name,omitempty"`
	In           string `json:"in,omitempty"`
	Description  string `json:"description,omitempty"`
}

// extractPathParams extracts parameters from a path like /sessions/{sessionId}.
func extractPathParams(path string) []Parameter {
	var params []Parameter
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			params = append(params, Parameter{
				Name:        name,
				In:          "path",
				Description: fmt.Sprintf("The %s identifier", name),
				Required:    true,
				Schema:      &Schema{Type: "string"},
			})
		}
	}
	return params
}

// handleOpenAPISpec serves the OpenAPI JSON specification, generated from
// this server's own router (the single source of truth — see openapi_router.go).
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	router := s.router
	if router == nil {
		// Server constructed without New() (tests); walk a hermetic router
		// so the document still describes the served route table.
		hermetic := NewHermeticServer(s.version)
		defer hermetic.Stop()
		router = hermetic.Router()
	}
	spec, err := buildRouterOpenAPISpec(router, s.version, s.documentationBaseURL(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	specBytes, err := json.Marshal(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// CORS is governed by corsMiddlewareFunc (allowlisted origins only — no wildcard "*").
	w.Header().Set("Content-Type", "application/json")
	w.Write(specBytes)
}

// handleSwaggerUI serves an airgapped, zero-CDN interactive OpenAPI explorer page.
// It makes zero third-party network requests so /docs works identically in
// offline/airgapped deployments without unpinned external CDN scripts.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	specURL := s.documentationBaseURL(r) + "/api/v1/openapi.json"

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>NTM API Documentation</title>
    <style>
        :root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; }
        body { margin: 0; padding: 1.5rem; background: #0f172a; color: #e2e8f0; }
        .wrap { max-width: 1080px; margin: 0 auto; }
        header { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 1rem; border-bottom: 1px solid #334155; padding-bottom: 1rem; margin-bottom: 1rem; }
        h1 { margin: 0; font-size: 1.35rem; }
        .meta { color: #94a3b8; font-size: 0.85rem; }
        .controls { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-bottom: 1rem; }
        input, button { padding: 0.45rem 0.7rem; border-radius: 0.375rem; border: 1px solid #475569; background: #1e293b; color: #f8fafc; font-size: 0.85rem; }
        input { flex: 1; min-width: 220px; }
        button { cursor: pointer; background: #2563eb; border-color: #2563eb; font-weight: 600; }
        button:hover { background: #1d4ed8; }
        .ep { border: 1px solid #334155; border-radius: 0.5rem; margin-bottom: 0.5rem; background: #1e293b; overflow: hidden; }
        .ep summary { cursor: pointer; padding: 0.65rem 0.85rem; display: flex; align-items: center; gap: 0.6rem; font-family: ui-monospace, SFMono-Regular, monospace; font-size: 0.85rem; }
        .badge { padding: 0.15rem 0.45rem; border-radius: 0.25rem; font-weight: 700; font-size: 0.72rem; text-transform: uppercase; }
        .GET { background: #0369a1; color: #e0f2fe; }
        .POST { background: #15803d; color: #dcfce7; }
        .PUT, .PATCH { background: #b45309; color: #fef3c7; }
        .DELETE { background: #b91c1c; color: #fee2e2; }
        .ep-body { padding: 0.75rem 0.85rem; border-top: 1px solid #334155; font-size: 0.82rem; color: #cbd5e1; }
        pre { background: #020617; padding: 0.65rem; border-radius: 0.375rem; overflow-x: auto; font-size: 0.78rem; }
        .err { padding: 0.75rem; border-radius: 0.5rem; background: #450a0a; border: 1px solid #991b1b; color: #fecaca; }
    </style>
</head>
<body>
    <div id="swagger-ui" class="wrap" data-spec-url=%q>
        <header>
            <div>
                <h1>NTM API Documentation</h1>
                <div id="spec-meta" class="meta">Airgapped OpenAPI 3.1 Explorer · zero external CDN dependencies</div>
            </div>
            <a id="raw-spec-link" href=%q style="color:#60a5fa;font-family:monospace;font-size:0.85rem;">openapi.json</a>
        </header>
        <div class="controls">
            <input id="filter-input" type="search" placeholder="Filter endpoints by path, method, or tag..." aria-label="Filter endpoints" />
            <input id="bearer-input" type="password" placeholder="Optional Bearer token / API key" aria-label="Bearer token" />
            <button id="reload-btn" type="button">Load Spec</button>
        </div>
        <div id="endpoint-list">Loading OpenAPI specification...</div>
    </div>
    <script>
        (function() {
            const specUrl = %q;
            const listEl = document.getElementById('endpoint-list');
            const metaEl = document.getElementById('spec-meta');
            const filterEl = document.getElementById('filter-input');
            const tokenEl = document.getElementById('bearer-input');
            const reloadBtn = document.getElementById('reload-btn');
            let cachedOps = [];

            function escapeHtml(s) {
                return String(s == null ? '' : s)
                    .replace(/&/g, '&amp;')
                    .replace(/</g, '&lt;')
                    .replace(/>/g, '&gt;')
                    .replace(/"/g, '&quot;');
            }

            function render() {
                const q = (filterEl.value || '').trim().toLowerCase();
                const items = cachedOps.filter(function(op) {
                    if (!q) return true;
                    return op.path.toLowerCase().includes(q) ||
                        op.method.toLowerCase().includes(q) ||
                        (op.summary || '').toLowerCase().includes(q) ||
                        (op.tags || []).join(' ').toLowerCase().includes(q);
                });
                if (!items.length) {
                    listEl.innerHTML = '<div class="meta">No endpoints match filter.</div>';
                    return;
                }
                listEl.innerHTML = items.map(function(op) {
                    const details = JSON.stringify({
                        operationId: op.operationId,
                        tags: op.tags,
                        parameters: op.parameters,
                        responses: op.responses
                    }, null, 2);
                    return '<details class="ep"><summary>' +
                        '<span class="badge ' + escapeHtml(op.method) + '">' + escapeHtml(op.method) + '</span>' +
                        '<span>' + escapeHtml(op.path) + '</span>' +
                        '<span class="meta">' + escapeHtml(op.summary || '') + '</span>' +
                        '</summary><div class="ep-body"><pre>' + escapeHtml(details) + '</pre></div></details>';
                }).join('');
            }

            async function loadSpec() {
                listEl.textContent = 'Loading OpenAPI specification...';
                try {
                    const headers = {};
                    const tok = (tokenEl.value || '').trim();
                    if (tok) headers['Authorization'] = 'Bearer ' + tok;
                    const res = await fetch(specUrl, { headers: headers });
                    if (!res.ok) throw new Error('HTTP ' + res.status + ' loading ' + specUrl);
                    const spec = await res.json();
                    const ops = [];
                    const paths = spec.paths || {};
                    Object.keys(paths).sort().forEach(function(p) {
                        const item = paths[p] || {};
                        ['get','post','put','patch','delete'].forEach(function(m) {
                            if (item[m]) {
                                ops.push({
                                    method: m.toUpperCase(),
                                    path: p,
                                    summary: item[m].summary || item[m].description || '',
                                    operationId: item[m].operationId || '',
                                    tags: item[m].tags || [],
                                    parameters: item[m].parameters || [],
                                    responses: item[m].responses || {}
                                });
                            }
                        });
                    });
                    cachedOps = ops;
                    metaEl.textContent = (spec.info && spec.info.title ? spec.info.title : 'NTM API') +
                        ' v' + (spec.info && spec.info.version ? spec.info.version : 'unknown') +
                        ' · ' + ops.length + ' endpoints · airgapped (zero CDN)';
                    render();
                } catch (err) {
                    listEl.innerHTML = '<div class="err">' + escapeHtml(err && err.message ? err.message : err) + '</div>';
                }
            }

            filterEl.addEventListener('input', render);
            reloadBtn.addEventListener('click', loadSpec);
            loadSpec();
        })();
    </script>
</body>
</html>`, specURL, specURL, specURL)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
	w.Write([]byte(html))
}

// documentationBaseURL returns the API base URL embedded in generated docs.
// A configured public base URL takes precedence because the listener host and
// port are often private when the server is deployed behind a reverse proxy.
func (s *Server) documentationBaseURL(r *http.Request) string {
	if s.publicBaseURL != "" {
		return strings.TrimRight(s.publicBaseURL, "/")
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, s.host, s.port)
}
