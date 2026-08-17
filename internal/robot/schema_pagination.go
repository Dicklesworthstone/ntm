// Package robot provides machine-readable output for AI agents.
// schema_pagination.go is the SINGLE canonical declaration point for
// per-surface pagination flags (WS0-G6 / bd-ws3-contract-breadth-psvyu.1).
//
// Every list-shaped registered schema type (a type with at least one direct,
// JSON-visible slice field that is not []byte) MUST appear in SchemaPagination
// exactly once, flagged either:
//
//   - Paginated: true  — the surface implements the offset/limit pagination
//     contract: a *PaginationInfo `json:"pagination"` field plus
//     _agent_hints.next_offset (see pagination.go).
//   - Paginated: false — UNPAGINATED BY DESIGN; Reason records why (bounded
//     cardinality, per-request result echo, cursor-based continuation, ...).
//
// SchemaPaginationViolations enforces exhaustiveness and contract fidelity by
// walking the schema registry; conformance tests in internal/robot and
// internal/cli fail on any unflagged list type, stale flag, or flagged-true
// type missing the contract fields. New envelope types are therefore covered
// at registration time by construction.
package robot

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// SchemaPaginationFlag declares the pagination posture of one schema type.
type SchemaPaginationFlag struct {
	// Paginated reports whether the surface implements offset/limit
	// pagination (PaginationInfo + next_offset hints).
	Paginated bool
	// Reason documents why an unpaginated list surface is bounded by design,
	// or names the contract for paginated ones.
	Reason string
}

// SchemaPagination is the single-declaration pagination flag map for all
// built-in schema types. Packages that register schema commands via
// MustRegisterSchemaCommand declare their flags with
// MustRegisterSchemaPagination alongside the registration.
var SchemaPagination = map[string]SchemaPaginationFlag{
	// --- Paginated surfaces (offset/limit PaginationInfo contract) ---------
	"status":         {Paginated: true, Reason: "sessions array pages via --robot-offset/--limit"},
	"snapshot":       {Paginated: true, Reason: "sessions array pages via --robot-offset/--limit"},
	"history":        {Paginated: true, Reason: "entries array pages via --limit/--offset"},
	"ensemble_modes": {Paginated: true, Reason: "modes array pages via --limit/--offset"},

	// --- Unpaginated by design ---------------------------------------------
	"accounts_list":      {Reason: "bounded: configured provider accounts (handful)"},
	"ack":                {Reason: "bounded: per-request acknowledgement results for addressed panes"},
	"activity":           {Reason: "bounded: one entry per live agent pane"},
	"agent_names":        {Reason: "bounded: one entry per live agent pane"},
	"alerts":             {Reason: "bounded: active alerts capped by alert engine retention"},
	"answer_dialog":      {Reason: "bounded: per-request dialog answer results"},
	"assign":             {Reason: "bounded: per-request assignment results"},
	"attention":          {Reason: "cursor-based continuation (attention cursor), not offset pagination"},
	"auto_restart_stuck": {Reason: "bounded: per-request restart results for stuck panes"},
	"bead_create":        {Reason: "bounded: per-request creation echo"},
	"bead_show":          {Reason: "bounded: single bead detail with dependency arrays"},
	"beads_list":         {Reason: "bounded via --limit truncation; total reported, no offset paging"},
	"bulk_assign":        {Reason: "bounded: per-request assignment plan echo"},
	"capabilities":       {Reason: "bounded: fixed command catalog"},
	"cass_context":       {Reason: "bounded: top-K relevant sessions from CASS"},
	"cass_insights":      {Reason: "bounded: top-K insights from CASS"},
	"cass_search":        {Reason: "bounded: top-K search hits from CASS"},
	"causality":          {Reason: "bounded: causal chain for one incident window"},
	"context":            {Reason: "bounded: per-pane context usage rows"},
	"dashboard":          {Reason: "bounded: dashboard sections are per-section capped"},
	"diagnose":           {Reason: "bounded: diagnostic findings for one probe run"},
	"dialogs":            {Reason: "bounded: currently open dialogs across panes"},
	"diff":               {Reason: "bounded: pane-vs-pane comparison rows"},
	"digest":             {Reason: "bounded: condensed attention digest, per-section capped"},
	"dismiss_alert":      {Reason: "bounded: per-request dismissal echo"},
	"docs":               {Reason: "bounded: fixed documentation topics"},
	"ensemble_presets":   {Reason: "bounded: fixed preset catalog"},
	"ensemble_suggest":   {Reason: "bounded: top-K suggestions"},
	"env":                {Reason: "bounded: environment diagnostic rows"},
	"errors":             {Reason: "bounded: recent errors capped by --limit truncation"},
	"events":             {Reason: "cursor-based continuation (event cursor + --limit), not offset pagination"},
	"exit_cli":           {Reason: "bounded: per-request exit results for addressed panes"},
	"files":              {Reason: "bounded: recent file activity capped per response"},
	"health":             {Reason: "bounded: one row per source/adapter"},
	"health_oauth":       {Reason: "bounded: one row per provider credential"},
	"inspect_agent":      {Reason: "bounded: single-agent inspection detail"},
	"inspect_coordination": {
		Reason: "bounded: coordination snapshot (reservations/messages) per-section capped",
	},
	"inspect_incident": {Reason: "bounded: single-incident evidence detail"},
	"inspect_quota":    {Reason: "bounded: single-scope quota detail"},
	"inspect_session":  {Reason: "bounded: one entry per pane in one session"},
	"inspect_work":     {Reason: "bounded: single work-item detail"},
	"interrupt":        {Reason: "bounded: per-request interrupt results for addressed panes"},
	"jfp_export":       {Reason: "bounded: per-request export file list"},
	"jfp_install":      {Reason: "bounded: per-request install results"},
	"kill_agent":       {Reason: "bounded: per-request kill results"},
	"kill_pane":        {Reason: "bounded: per-request kill results"},
	"logs":             {Reason: "bounded: recent log lines capped by --limit truncation"},
	"mail":             {Reason: "bounded: unread mail summary capped per response"},
	"mail_check":       {Reason: "bounded: per-session mail check rows"},
	"metrics":          {Reason: "bounded: current metric snapshot rows"},
	"monitor":          {Reason: "bounded: one row per monitored pane"},
	"palette":          {Reason: "bounded: fixed palette command catalog"},
	"pane_address":     {Reason: "bounded: address forms for one pane"},
	"plan":             {Reason: "bounded: execution tracks for one plan request"},
	"probe":            {Reason: "bounded: probe findings for one session"},
	"productivity":     {Reason: "bounded: per-agent productivity rows for one window"},
	"rch_workers":      {Reason: "bounded: one row per remote compilation worker"},
	"recipes":          {Reason: "bounded: fixed recipe catalog"},
	"replay":           {Reason: "bounded: replay window capped by ring buffer"},
	"restart_pane":     {Reason: "bounded: per-request restart echo"},
	"route":            {Reason: "bounded: per-request routing decision echo"},
	"ru_sync":          {Reason: "bounded: per-repo sync results for one run"},
	"safety_simulate":  {Reason: "bounded: per-request simulated command steps"},
	"schema":           {Reason: "bounded: fixed schema catalog"},
	"send":             {Reason: "bounded: per-request send results for addressed panes"},
	"send_receipt":     {Reason: "bounded: delivery receipt for one send"},
	"spawn":            {Reason: "bounded: per-request spawned pane rows"},
	"support_bundle":   {Reason: "bounded: bundle manifest for one collection run"},
	"terse":            {Reason: "bounded: extreme-compression summary"},
	"tokens":           {Reason: "bounded: per-pane token usage rows"},
	"tools":            {Reason: "bounded: fixed tool catalog"},
	"triage":           {Reason: "bounded: top-K triage recommendations"},
	"wait":             {Reason: "bounded: wake reasons for one wait"},
	"watch_bead":       {Reason: "bounded: events for one watched bead window"},
	"xf_search":        {Reason: "bounded: top-K transcript search hits"},
}

// MustRegisterSchemaPagination declares the pagination flag for a schema type
// registered by another package (via MustRegisterSchemaCommand). Like schema
// command registration it must happen during package initialization, before
// the registry is first read.
func MustRegisterSchemaPagination(name string, flag SchemaPaginationFlag) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "-", "_")
	if name == "" {
		panic("robot: schema pagination flag name is empty") // ubs:ignore -- invalid package initialization must fail fast
	}
	robotRegistryBuildMu.Lock()
	defer robotRegistryBuildMu.Unlock()
	if robotRegistry != nil {
		panic(fmt.Sprintf("robot: schema pagination flag %q registered after registry initialization", name)) // ubs:ignore -- init ordering is a programming error
	}
	if existing, ok := SchemaPagination[name]; ok && existing != flag {
		panic(fmt.Sprintf("robot: schema pagination flag %q already declared as %+v", name, existing)) // ubs:ignore -- conflicting declarations are programming errors
	}
	SchemaPagination[name] = flag
}

var paginationInfoType = reflect.TypeOf(PaginationInfo{})

// SchemaTypeListShaped reports whether a schema binding type has at least one
// direct, JSON-visible slice field (excluding []byte). Such types must carry
// a pagination flag.
func SchemaTypeListShaped(typ reflect.Type) bool {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() || field.Anonymous || field.Tag.Get("json") == "-" {
			continue
		}
		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice && ft.Elem().Kind() != reflect.Uint8 {
			return true
		}
	}
	return false
}

func schemaTypeHasPaginationField(typ reflect.Type) bool {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft == paginationInfoType {
			return true
		}
	}
	return false
}

// SchemaPaginationViolations walks the robot schema registry and returns one
// message per pagination-flag contract violation:
//
//   - a list-shaped schema type with no SchemaPagination entry
//   - a SchemaPagination entry for a type that is not list-shaped (stale)
//   - a Paginated=true type missing the PaginationInfo contract field
//   - an unpaginated flag on a surface whose registry Boundedness metadata
//     still claims SupportsPagination (the registry contradicting itself)
//
// It is exported (not test-only) so both internal/robot and downstream
// registrars (internal/cli) can assert emptiness after their registrations.
func SchemaPaginationViolations() []string {
	registry := GetRobotRegistry()
	var violations []string

	seen := make(map[string]bool, len(registry.SchemaTypes))
	for _, name := range registry.SchemaTypes {
		seen[name] = true
		binding, ok := registry.SchemaBinding(name)
		if !ok {
			violations = append(violations, fmt.Sprintf("schema type %q has no binding", name))
			continue
		}
		typ := reflect.TypeOf(binding)
		flag, flagged := SchemaPagination[name]
		listShaped := SchemaTypeListShaped(typ)
		switch {
		case listShaped && !flagged:
			violations = append(violations, fmt.Sprintf("list-shaped schema type %q is not flagged in SchemaPagination (declare Paginated true/false with a reason)", name))
		case !listShaped && flagged:
			violations = append(violations, fmt.Sprintf("SchemaPagination flags %q but the type has no list field (stale flag)", name))
		case flagged && flag.Paginated && !schemaTypeHasPaginationField(typ):
			violations = append(violations, fmt.Sprintf("schema type %q is flagged Paginated but has no PaginationInfo contract field", name))
		case flagged && !flag.Paginated && strings.TrimSpace(flag.Reason) == "":
			violations = append(violations, fmt.Sprintf("schema type %q is flagged unpaginated without a reason", name))
		}
	}

	for name := range SchemaPagination {
		if !seen[name] {
			violations = append(violations, fmt.Sprintf("SchemaPagination flags %q which is not a registered schema type", name))
		}
	}

	for _, surface := range registry.Surfaces {
		if surface.SchemaType == "" || surface.Boundedness == nil || !surface.Boundedness.SupportsPagination {
			continue
		}
		if flag, ok := SchemaPagination[surface.SchemaType]; ok && !flag.Paginated {
			violations = append(violations, fmt.Sprintf("surface %q Boundedness claims SupportsPagination but SchemaPagination flags %q unpaginated", surface.Name, surface.SchemaType))
		}
	}

	sort.Strings(violations)
	return violations
}
