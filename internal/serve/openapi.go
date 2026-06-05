// Package serve provides HTTP server functionality including OpenAPI spec generation.
package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
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

// GenerateOpenAPISpec generates an OpenAPI 3.1 spec from the kernel registry.
func GenerateOpenAPISpec(version, serverURL string) *OpenAPISpec {
	commands := kernel.List()

	spec := &OpenAPISpec{
		OpenAPI: "3.1.0",
		Info: OpenAPIInfo{
			Title:       "NTM REST API",
			Version:     version,
			Description: "REST API for NTM (Named Tmux Manager). Generated from the kernel command registry.",
		},
		Servers: []OpenAPIServer{
			{URL: serverURL, Description: "NTM server"},
		},
		Paths: make(map[string]PathItem),
		Components: &OpenAPIComponents{
			Schemas: map[string]*Schema{
				"SuccessResponse": {
					Type: "object",
					Properties: map[string]*Schema{
						"success":   {Type: "boolean"},
						"message":   {Type: "string"},
						"timestamp": {Type: "string", Format: "date-time"},
					},
					Required: []string{"success", "timestamp"},
				},
				"ErrorResponse": {
					Type: "object",
					Properties: map[string]*Schema{
						"success":    {Type: "boolean"},
						"error":      {Type: "string"},
						"error_code": {Type: "string"},
						"timestamp":  {Type: "string", Format: "date-time"},
					},
					Required: []string{"success", "error", "error_code", "timestamp"},
				},
			},
			SecuritySchemes: map[string]*SecurityScheme{
				"bearerAuth": {
					Type:         "http",
					Scheme:       "bearer",
					BearerFormat: "JWT",
					Description:  "Bearer token authentication",
				},
				"apiKey": {
					Type:        "apiKey",
					Name:        "X-API-Key",
					In:          "header",
					Description: "API key authentication via X-API-Key header",
				},
			},
		},
	}

	// Collect tags from categories
	tagSet := make(map[string]bool)

	for _, cmd := range commands {
		if cmd.REST == nil {
			continue
		}

		// Build the path with proper parameter syntax
		path := normalizePathForOpenAPI(cmd.REST.Path)

		// Extract path parameters
		params := extractPathParams(path)

		// Create operation
		op := &Operation{
			Summary:     cmd.Description,
			Description: buildDescription(cmd),
			OperationID: strings.ReplaceAll(cmd.Name, ".", "_"),
			Parameters:  params,
			Responses: map[string]Response{
				"200": {
					Description: "Successful operation",
					Content: map[string]MediaType{
						"application/json": {
							Schema: buildResponseSchema(cmd),
						},
					},
				},
				"400": {
					Description: "Bad request",
					Content: map[string]MediaType{
						"application/json": {
							Schema: &Schema{Ref: "#/components/schemas/ErrorResponse"},
						},
					},
				},
				"401": {
					Description: "Unauthorized",
					Content: map[string]MediaType{
						"application/json": {
							Schema: &Schema{Ref: "#/components/schemas/ErrorResponse"},
						},
					},
				},
				"500": {
					Description: "Internal server error",
					Content: map[string]MediaType{
						"application/json": {
							Schema: &Schema{Ref: "#/components/schemas/ErrorResponse"},
						},
					},
				},
			},
		}

		// Add tag based on category
		if cmd.Category != "" {
			op.Tags = []string{cmd.Category}
			tagSet[cmd.Category] = true
		}

		// Add request body for non-GET methods
		if cmd.Input != nil && cmd.REST.Method != "GET" {
			op.RequestBody = &RequestBody{
				Required: true,
				Content: map[string]MediaType{
					"application/json": {
						Schema: buildInputSchema(cmd),
					},
				},
			}
		}

		// Add examples from command
		if len(cmd.Examples) > 0 {
			examples := make(map[string]ExampleRef)
			for _, ex := range cmd.Examples {
				examples[ex.Name] = ExampleRef{
					Summary: ex.Description,
					Value:   ex.Output,
				}
			}
			if resp, ok := op.Responses["200"]; ok {
				if content, ok := resp.Content["application/json"]; ok {
					content.Examples = examples
					resp.Content["application/json"] = content
					op.Responses["200"] = resp
				}
			}
		}

		// Add to paths
		pathItem, exists := spec.Paths[path]
		if !exists {
			pathItem = PathItem{}
		}

		switch strings.ToUpper(cmd.REST.Method) {
		case "GET":
			pathItem.Get = op
		case "POST":
			pathItem.Post = op
		case "PUT":
			pathItem.Put = op
		case "PATCH":
			pathItem.Patch = op
		case "DELETE":
			pathItem.Delete = op
		}

		spec.Paths[path] = pathItem
	}

	// Build sorted tag list
	var tags []string
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	for _, tag := range tags {
		spec.Tags = append(spec.Tags, OpenAPITag{
			Name:        tag,
			Description: fmt.Sprintf("Operations related to %s", tag),
		})
	}

	return spec
}

// normalizePathForOpenAPI converts paths like /sessions/{sessionId} to OpenAPI format.
func normalizePathForOpenAPI(path string) string {
	// Ensure path starts with /api/v1 prefix if not already present
	if !strings.HasPrefix(path, "/api/") {
		path = "/api/v1" + path
	}
	return path
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

// buildDescription builds a detailed description from command metadata.
func buildDescription(cmd kernel.Command) string {
	var parts []string
	parts = append(parts, cmd.Description)

	if cmd.SafetyLevel != "" {
		parts = append(parts, fmt.Sprintf("\n\n**Safety Level:** %s", cmd.SafetyLevel))
	}
	if cmd.Idempotent {
		parts = append(parts, "\n\n**Idempotent:** Yes - safe to retry")
	}
	if len(cmd.EmitsEvents) > 0 {
		parts = append(parts, fmt.Sprintf("\n\n**Emits Events:** %s", strings.Join(cmd.EmitsEvents, ", ")))
	}

	return strings.Join(parts, "")
}

// buildResponseSchema builds a schema reference for the response.
func buildResponseSchema(cmd kernel.Command) *Schema {
	if cmd.Output != nil && cmd.Output.Ref != "" {
		// Use schema reference if available
		schemaName := cmd.Output.Name
		if schemaName == "" {
			schemaName = strings.ReplaceAll(cmd.Name, ".", "_") + "_Response"
		}
		return &Schema{Ref: "#/components/schemas/" + schemaName}
	}
	return &Schema{Ref: "#/components/schemas/SuccessResponse"}
}

// buildInputSchema builds a schema reference for the request body.
func buildInputSchema(cmd kernel.Command) *Schema {
	if cmd.Input != nil && cmd.Input.Ref != "" {
		schemaName := cmd.Input.Name
		if schemaName == "" {
			schemaName = strings.ReplaceAll(cmd.Name, ".", "_") + "_Request"
		}
		return &Schema{Ref: "#/components/schemas/" + schemaName}
	}
	return &Schema{
		Type:                 "object",
		AdditionalProperties: true,
	}
}

// handleOpenAPISpec serves the OpenAPI JSON specification.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	serverURL := fmt.Sprintf("http://%s:%d", s.host, s.port)
	if r.TLS != nil {
		serverURL = fmt.Sprintf("https://%s:%d", s.host, s.port)
	}

	spec := GenerateOpenAPISpec("dev", serverURL)

	specBytes, err := json.Marshal(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(specBytes)
}

// handleSwaggerUI serves the Swagger UI HTML page.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	specURL := fmt.Sprintf("http://%s:%d/api/v1/openapi.json", s.host, s.port)
	if r.TLS != nil {
		specURL = fmt.Sprintf("https://%s:%d/api/v1/openapi.json", s.host, s.port)
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>NTM API Documentation</title>
    <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
    <style>
        html { box-sizing: border-box; overflow-y: scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin: 0; background: #fafafa; }
        .topbar { display: none; }
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
    <script>
        window.onload = function() {
            window.ui = SwaggerUIBundle({
                url: %q,
                dom_id: '#swagger-ui',
                deepLinking: true,
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
        };
    </script>
</body>
</html>`, specURL)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}
