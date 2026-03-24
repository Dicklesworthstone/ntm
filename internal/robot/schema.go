// Package robot provides machine-readable output for AI agents.
// schema.go provides JSON Schema generation for robot command outputs.
package robot

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// SchemaCommand is the supported schema command types.
var SchemaCommand = map[string]interface{}{
	// Core state inspection
	"status":         StatusOutput{},
	"snapshot":       SnapshotOutput{},
	"dashboard":      DashboardOutput{},
	"terse":          TerseOutput{},
	"summary":        SessionSummaryResponse{},
	"events":         EventsOutput{},
	"digest":         DigestOutput{},
	"attention":      AttentionOutput{},
	"overlay":        OverlayOutput{},
	"wait":           WaitResponse{},
	"version":        VersionOutput{},
	"mail":           MailOutput{},
	"history":        HistoryOutput{},
	"recipes":        RecipesOutput{},
	"alerts":         AlertsOutput{},
	"cass_status":    CASSStatusOutput{},
	"cass_search":    CASSSearchOutput{},
	"acfs_status":    ACFSStatusOutput{},
	"setup_status":   ACFSStatusOutput{},
	"jfp_status":     JFPStatusOutput{},
	"jfp_list":       JFPListOutput{},
	"jfp_search":     JFPSearchOutput{},
	"jfp_show":       JFPShowOutput{},
	"jfp_suggest":    JFPSuggestOutput{},
	"jfp_installed":  JFPInstalledOutput{},
	"jfp_categories": JFPCategoriesOutput{},
	"jfp_tags":       JFPTagsOutput{},
	"jfp_bundles":    JFPBundlesOutput{},
	"jfp_install":    JFPInstallOutput{},
	"jfp_export":     JFPExportOutput{},
	"jfp_update":     JFPUpdateOutput{},
	"ms_search":      MSSearchOutput{},
	"ms_show":        MSShowOutput{},
	"dcg_status":     DCGStatusOutput{},
	"dcg_check":      DCGCheckOutput{},
	"slb_pending":    SLBPendingOutput{},
	"slb_approve":    SLBActionOutput{},
	"slb_deny":       SLBActionOutput{},
	"ru_sync":        RUSyncOutput{},
	"rano_stats":     RanoStatsOutput{},
	"rch_status":     RCHStatusOutput{},
	"rch_workers":    RCHWorkersOutput{},

	// Session operations
	"spawn":            SpawnOutput{},
	"send":             SendOutput{},
	"interrupt":        InterruptOutput{},
	"tail":             TailOutput{},
	"watch_bead":       WatchBeadOutput{},
	"ack":              AckOutput{},
	"activity":         ActivityOutput{},
	"errors":           ErrorsOutput{},
	"logs":             LogsOutput{},
	"agent_health":     AgentHealthOutput{},
	"is_working":       IsWorkingOutput{},
	"restart_pane":     RestartPaneOutput{},
	"smart_restart":    SmartRestartOutput{},
	"monitor":          MonitorOutput{},
	"health_oauth":     OAuthHealthOutput{},
	"agent_names":      AgentNamesOutput{},
	"route":            RouteOutput{},
	"bulk_assign":      BulkAssignOutput{},
	"context":          ContextOutput{},
	"proxy_status":     ProxyStatusOutput{},
	"support_bundle":   SupportBundleOutput{},
	"mail_check":       MailCheckOutput{},
	"env":              EnvOutput{},
	"quota_status":     QuotaStatusOutput{},
	"quota_check":      QuotaCheckOutput{},
	"account_status":   AccountStatusOutput{},
	"accounts_list":    AccountsListOutput{},
	"switch_account":   SwitchAccountOutput{},
	"xf_search":        XFSearchOutput{},
	"xf_status":        XFStatusOutput{},
	"files":            FilesOutput{},
	"metrics":          MetricsOutput{},
	"replay":           ReplayOutput{},
	"dismiss_alert":    DismissAlertOutput{},
	"docs":             DocsOutput{},
	"tools":            ToolsOutput{},
	"cass_context":     CASSContextOutput{},
	"cass_insights":    CASSInsightsOutput{},
	"ensemble_stop":    EnsembleStopOutput{},
	"ensemble_suggest": EnsembleSuggestOutput{},

	// Pane inspection
	"inspect":              InspectPaneOutput{},
	"inspect_session":      InspectSessionOutput{},
	"inspect_agent":        InspectAgentOutput{},
	"inspect_work":         InspectWorkOutput{},
	"inspect_coordination": InspectCoordinationOutput{},
	"inspect_quota":        InspectQuotaOutput{},
	"inspect_incident":     InspectIncidentOutput{},

	// Ensemble
	"ensemble":         EnsembleOutput{},
	"ensemble_spawn":   EnsembleSpawnOutput{},
	"ensemble_presets": EnsemblePresetsOutput{},
	"ensemble_modes":   EnsembleModesOutput{},

	// Beads/work management
	"beads_list":      BeadsListOutput{},
	"bead_claim":      BeadClaimOutput{},
	"bead_create":     BeadCreateOutput{},
	"bead_show":       BeadShowOutput{},
	"bead_close":      BeadCloseOutput{},
	"assign":          AssignOutput{},
	"plan":            PlanOutput{},
	"triage":          TriageOutput{},
	"graph":           GraphOutput{},
	"forecast":        ForecastOutput{},
	"suggest":         SuggestOutput{},
	"impact":          ImpactOutput{},
	"search":          SearchOutput{},
	"label_attention": LabelAttentionOutput{},
	"label_flow":      LabelFlowOutput{},
	"label_health":    LabelHealthOutput{},
	"file_beads":      FileBeadsOutput{},
	"file_hotspots":   FileHotspotsOutput{},
	"file_relations":  FileRelationsOutput{},

	// Health and diagnostics
	"health":             HealthOutput{},
	"diagnose":           DiagnoseOutput{},
	"probe":              ProbeSessionOutput{},
	"auto_restart_stuck": AutoRestartStuckOutput{},
}

// JSONSchema represents a JSON Schema document.
type JSONSchema struct {
	Schema      string                 `json:"$schema,omitempty"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	Type        string                 `json:"type,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Properties  map[string]*JSONSchema `json:"properties,omitempty"`
	Items       *JSONSchema            `json:"items,omitempty"`
	Definitions map[string]*JSONSchema `json:"definitions,omitempty"`
	Ref         string                 `json:"$ref,omitempty"`
	Format      string                 `json:"format,omitempty"`
	Enum        []interface{}          `json:"enum,omitempty"`
	Default     interface{}            `json:"default,omitempty"`
	// For additional type info
	AdditionalProperties *JSONSchema `json:"additionalProperties,omitempty"`
}

// SchemaOutput is the structured output for --robot-schema.
type SchemaOutput struct {
	RobotResponse
	SchemaType string        `json:"schema_type"`
	Schema     *JSONSchema   `json:"schema,omitempty"`
	Schemas    []*JSONSchema `json:"schemas,omitempty"` // For --robot-schema=all
}

// GetSchema generates JSON Schema for the specified type.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSchema(schemaType string) (*SchemaOutput, error) {
	registry := GetRobotRegistry()
	output := &SchemaOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaType:    schemaType,
	}

	if schemaType == "all" {
		// Generate all schemas
		schemaTypes := getSchemaTypes()
		schemas := make([]*JSONSchema, 0, len(schemaTypes))
		for _, name := range schemaTypes {
			typ, ok := registry.SchemaBinding(name)
			if !ok {
				continue
			}
			schema := generateSchema(typ, name)
			schemas = append(schemas, schema)
		}
		output.Schemas = schemas
	} else {
		// Generate single schema
		typ, ok := registry.SchemaBinding(schemaType)
		if !ok {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("unknown schema type: %s", schemaType),
				ErrCodeInvalidFlag,
				fmt.Sprintf("Available types: %s, all", strings.Join(getSchemaTypes(), ", ")),
			)
			return output, nil
		}
		output.Schema = generateSchema(typ, schemaType)
	}

	return output, nil
}

// PrintSchema generates and outputs JSON Schema for the specified type.
// This is a thin wrapper around GetSchema() for CLI output.
func PrintSchema(schemaType string) error {
	output, err := GetSchema(schemaType)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// getSchemaTypes returns available schema type names.
func getSchemaTypes() []string {
	if registry := GetRobotRegistry(); registry != nil && len(registry.SchemaTypes) != 0 {
		return cloneStrings(registry.SchemaTypes)
	}
	types := make([]string, 0, len(SchemaCommand))
	for name := range SchemaCommand {
		types = append(types, name)
	}
	return types
}

// generateSchema creates a JSON Schema from a Go type.
func generateSchema(v interface{}, name string) *JSONSchema {
	schema := &JSONSchema{
		Schema:      "http://json-schema.org/draft-07/schema#",
		Title:       fmt.Sprintf("NTM %s Output", capitalize(name)),
		Type:        "object",
		Properties:  make(map[string]*JSONSchema),
		Definitions: make(map[string]*JSONSchema),
	}

	if surface, ok := registrySurfaceForSchemaType(GetRobotRegistry(), name); ok {
		schema.Title = fmt.Sprintf("NTM %s Output", humanizeRobotRegistryName(surface.Name))
		if strings.TrimSpace(surface.Description) != "" {
			schema.Description = surface.Description
		}
	}

	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	var required []string
	processStruct(t, schema.Properties, &required, schema.Definitions)
	schema.Required = required

	return schema
}

func registrySurfaceForSchemaType(registry *RobotRegistry, schemaType string) (RobotSurfaceDescriptor, bool) {
	if registry == nil {
		return RobotSurfaceDescriptor{}, false
	}
	normalized := normalizeRobotRegistryName(schemaType)
	for _, surface := range registry.Surfaces {
		if surface.SchemaType == schemaType || normalizeRobotRegistryName(surface.Name) == normalized {
			return surface, true
		}
	}
	return RobotSurfaceDescriptor{}, false
}

// processStruct extracts schema properties from a struct type.
func processStruct(t reflect.Type, props map[string]*JSONSchema, required *[]string, defs map[string]*JSONSchema) {
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Handle embedded structs (like RobotResponse)
		if field.Anonymous {
			processStruct(field.Type, props, required, defs)
			continue
		}

		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		fieldName, omitempty := parseJSONTag(jsonTag)
		if fieldName == "" {
			fieldName = field.Name
		}

		prop := typeToSchema(field.Type, defs)

		// Add description from field name if not set
		if prop.Description == "" {
			prop.Description = generateDescription(field.Name)
		}

		props[fieldName] = prop

		// If not omitempty, it's required
		if !omitempty {
			*required = append(*required, fieldName)
		}
	}
}

// parseJSONTag parses a json struct tag.
func parseJSONTag(tag string) (name string, omitempty bool) {
	if tag == "" {
		return "", false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, part := range parts[1:] {
		if part == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

// typeToSchema converts a Go type to a JSON Schema.
func typeToSchema(t reflect.Type, defs map[string]*JSONSchema) *JSONSchema {
	// Handle pointers
	if t.Kind() == reflect.Ptr {
		schema := typeToSchema(t.Elem(), defs)
		// Pointer types can be null
		return schema
	}

	switch t.Kind() {
	case reflect.Bool:
		return &JSONSchema{Type: "boolean"}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &JSONSchema{Type: "integer"}

	case reflect.Float32, reflect.Float64:
		return &JSONSchema{Type: "number"}

	case reflect.String:
		return &JSONSchema{Type: "string"}

	case reflect.Slice:
		return &JSONSchema{
			Type:  "array",
			Items: typeToSchema(t.Elem(), defs),
		}

	case reflect.Map:
		return &JSONSchema{
			Type:                 "object",
			AdditionalProperties: typeToSchema(t.Elem(), defs),
		}

	case reflect.Struct:
		// Special handling for time.Time
		if t == reflect.TypeOf(time.Time{}) {
			return &JSONSchema{
				Type:        "string",
				Format:      "date-time",
				Description: "RFC3339 timestamp",
			}
		}

		// For other structs, create a reference
		typeName := t.Name()
		if typeName == "" {
			// Anonymous struct, inline it
			schema := &JSONSchema{
				Type:       "object",
				Properties: make(map[string]*JSONSchema),
			}
			var required []string
			processStruct(t, schema.Properties, &required, defs)
			schema.Required = required
			return schema
		}

		// Add to definitions if not already there
		if _, exists := defs[typeName]; !exists {
			schema := &JSONSchema{
				Type:       "object",
				Properties: make(map[string]*JSONSchema),
			}
			var required []string
			processStruct(t, schema.Properties, &required, defs)
			schema.Required = required
			defs[typeName] = schema
		}

		return &JSONSchema{
			Ref: fmt.Sprintf("#/definitions/%s", typeName),
		}

	case reflect.Interface:
		// Interface{} means any type
		return &JSONSchema{}

	default:
		return &JSONSchema{Type: "string"}
	}
}

// generateDescription creates a human-readable description from a field name.
func generateDescription(name string) string {
	// Convert CamelCase to words
	var words []string
	var current strings.Builder

	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			words = append(words, current.String())
			current.Reset()
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}

	// Join and lowercase
	desc := strings.Join(words, " ")
	if len(desc) > 0 {
		desc = strings.ToUpper(desc[:1]) + strings.ToLower(desc[1:])
	}

	return desc
}
