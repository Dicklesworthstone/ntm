// Package serve provides HTTP server functionality including OpenAPI spec generation.
package serve

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// These pinned Swagger UI assets are shipped with the binary so documentation
// works without internet access. Licenses and source hashes live beside them.
//
//go:embed swagger/swagger-ui.css swagger/swagger-ui-bundle.js swagger/swagger-ui-standalone-preset.js
var swaggerAssets embed.FS

const swaggerVersion = "5.35.1"

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
	Enum                 []any              `json:"enum,omitempty"`
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
	spec, err := buildRouterOpenAPISpec(router, s.version, s.documentationBaseURL())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	specBytes, err := json.Marshal(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(specBytes)
}

// handleSwaggerUI serves the offline Swagger UI and its three embedded assets.
// Asset requests use the existing public /docs route, with an exact allowlist;
// they cannot expose other embedded files or filesystem paths.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	if asset := r.URL.Query().Get("asset"); asset != "" {
		switch asset {
		case "swagger-ui.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case "swagger-ui-bundle.js", "swagger-ui-standalone-preset.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		default:
			http.NotFound(w, r)
			return
		}
		data, err := swaggerAssets.ReadFile("swagger/" + asset)
		if err != nil {
			http.Error(w, "documentation asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", fmt.Sprintf("\"swagger-%s-%s\"", swaggerVersion, asset))
		http.ServeContent(w, r, asset, time.Time{}, bytes.NewReader(data))
		return
	}

	var random [18]byte
	if _, err := rand.Read(random[:]); err != nil {
		http.Error(w, "documentation unavailable", http.StatusInternalServerError)
		return
	}
	// The URL-safe alphabet is also valid in CSP nonces and needs no HTML
	// character escaping when the same nonce is rendered into script attributes.
	nonce := base64.RawURLEncoding.EncodeToString(random[:])
	baseURL := strings.TrimRight(s.documentationBaseURL(), "/")
	data := struct {
		SpecURL   string
		AssetsURL string
		Nonce     string
		Version   string
	}{baseURL + "/api/v1/openapi.json", baseURL + "/docs", nonce, swaggerVersion}

	// Buffer rendering so a template failure cannot return a partial 200 page.
	var html bytes.Buffer
	if err := swaggerPage.Execute(&html, data); err != nil {
		http.Error(w, "documentation unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	assetSources := "'self'"
	if publicURL, err := url.Parse(s.publicBaseURL); err == nil && (publicURL.Scheme == "http" || publicURL.Scheme == "https") && publicURL.Host != "" {
		// Public URLs may point at a reverse proxy. Permit only that configured
		// origin, never request-supplied hosts or arbitrary CSP source syntax.
		invalidHost := strings.IndexFunc(publicURL.Host, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c))
		}) >= 0
		if !invalidHost {
			assetSources += " " + publicURL.Scheme + "://" + publicURL.Host
		}
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; script-src 'nonce-"+nonce+"'; style-src "+assetSources+" 'unsafe-inline'; connect-src "+assetSources+"; img-src "+assetSources+" data:; font-src 'self' data:")
	w.Write(html.Bytes())
}

var swaggerPage = template.Must(template.New("swagger").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>NTM API Documentation</title>
    <link rel="stylesheet" type="text/css" href="{{.AssetsURL}}?asset=swagger-ui.css&amp;v={{.Version}}">
    <style>
        html { box-sizing: border-box; overflow-y: scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin: 0; background: #fafafa; }
        .topbar { display: none; }
        #docs-auth { padding: 16px; font-family: sans-serif; background: #fff; border-bottom: 1px solid #ddd; }
        #docs-auth input, #docs-auth select, #docs-auth button { margin: 4px; padding: 8px; }
        #docs-status { display: block; margin: 8px 4px; }
    </style>
</head>
<body>
    <form id="docs-auth">
        <label>Authentication <select id="docs-auth-mode"><option value="none">Local / client certificate</option><option value="bearer">Bearer token</option><option value="api_key">API key</option></select></label>
        <label>Credential <input id="docs-credential" type="password" autocomplete="off" spellcheck="false"></label>
        <button type="submit">Load API documentation</button>
        <span id="docs-status" role="status">Credentials stay in this page and are sent only to this API.</span>
    </form>
    <div id="swagger-ui"></div>
    <script nonce="{{.Nonce}}" src="{{.AssetsURL}}?asset=swagger-ui-bundle.js&amp;v={{.Version}}"></script>
    <script nonce="{{.Nonce}}" src="{{.AssetsURL}}?asset=swagger-ui-standalone-preset.js&amp;v={{.Version}}"></script>
    <script nonce="{{.Nonce}}">
        const specURL = new URL({{.SpecURL}}, location.href);
        const apiBase = new URL(specURL.href);
        apiBase.pathname = apiBase.pathname.slice(0, -'/api/v1/openapi.json'.length) || '/';
        let credentials = {};
        function authorize(request) {
            const target = new URL(request.url, location.href);
            // Never attach credentials to external references or a sibling API
            // outside a configured reverse-proxy path prefix.
            if (target.origin === apiBase.origin && target.pathname.startsWith(apiBase.pathname.replace(/\/$/, '') + '/')) {
                request.headers = Object.assign({}, request.headers, credentials);
                // A redirect could forward an API key to a sibling service
                // outside this prefix. Swagger passes this option to fetch.
                request.redirect = 'error';
            }
            return request;
        }
        async function loadDocumentation(event) {
            if (event) event.preventDefault();
            const mode = document.getElementById('docs-auth-mode').value;
            const credential = document.getElementById('docs-credential').value.trim();
            credentials = mode === 'bearer' ? {Authorization: 'Bearer ' + credential} : mode === 'api_key' ? {'X-API-Key': credential} : {};
            const status = document.getElementById('docs-status');
            status.textContent = 'Loading API documentation…';
            try {
                const response = await fetch(specURL, {headers: credentials, cache: 'no-store', redirect: 'error'});
                if (!response.ok) throw new Error('HTTP ' + response.status + ': enter an authorized API credential and load again.');
                const spec = await response.json();
                window.ui = SwaggerUIBundle({
                spec: spec,
                dom_id: '#swagger-ui',
                deepLinking: true,
                validatorUrl: null,
                requestInterceptor: authorize,
                persistAuthorization: false,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIStandalonePreset
                ],
                plugins: [
                    SwaggerUIBundle.plugins.DownloadUrl
                ],
                layout: "StandaloneLayout",
                defaultModelsExpandDepth: 1,
                defaultModelExpandDepth: 1,
                docExpansion: "list",
                filter: true,
                showExtensions: true,
                showCommonExtensions: true
                });
                status.textContent = 'Loaded ' + Object.keys(spec.paths || {}).length + ' API paths. Credentials are not saved.';
            } catch (error) {
                document.getElementById('swagger-ui').replaceChildren();
                status.textContent = error.message;
            }
        }
        document.getElementById('docs-auth').addEventListener('submit', loadDocumentation);
        loadDocumentation();
    </script>
</body>
</html>`))

// documentationBaseURL returns the API base URL embedded in generated docs.
// A configured public base URL includes any reverse-proxy path prefix. By
// default, origin-relative URLs keep assets and authenticated API calls on the
// browser's origin, regardless of the listener's wildcard address, port, or TLS
// termination. No request Host or forwarded header is trusted as configuration.
func (s *Server) documentationBaseURL() string {
	if s.publicBaseURL != "" {
		return strings.TrimRight(s.publicBaseURL, "/")
	}
	return "/"
}
