package config

// WS0-G2 config-key liveness test (bd-ws0-guards-klz98.3).
//
// Walks every leaf key of the parsed Config struct; each must be claimed via
// config.RegisterReader (see liveness.go) or carry a line in
// ci/allowlists/config.txt tied to an OPEN bead. The ratchet runs in BOTH
// directions with the same semantics as scripts/guards/lib/ratchet.sh
// (implemented here in Go because the walk requires reflection over the
// struct): an unclaimed key with no allowlist line is NEW DEBT; an allowlist
// line whose key is now claimed (or gone) is STALE and must be deleted in the
// same PR. CI coverage: the ordinary `test` job (go test ./...) and the
// `reality-guards-go` job both run this; the bare reality-guards job runs
// only the format side via scripts/guards/config_liveness.sh.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// leafKeys returns the dotted TOML paths of every leaf key reachable from t.
// Rules: `toml:"-"` fields are skipped; nested structs (and pointers/slices/
// arrays of structs) recurse under their tag; time.Time, time.Duration, and
// every non-struct kind are leaves; maps are leaves (arbitrary user keys).
func leafKeys(t reflect.Type, prefix string, out map[string]bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		if prefix != "" {
			out[prefix] = true
		}
		return
	}
	if t.PkgPath() == "time" { // time.Time etc: opaque leaf
		out[prefix] = true
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("toml")
		if idx := strings.Index(tag, ","); idx >= 0 {
			tag = tag[:idx]
		}
		if tag == "-" {
			continue
		}
		name := tag
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.Struct:
			if ft == reflect.TypeOf(time.Time{}) || ft.PkgPath() == "time" {
				out[key] = true
			} else {
				leafKeys(ft, key, out)
			}
		case reflect.Slice, reflect.Array:
			et := ft.Elem()
			for et.Kind() == reflect.Ptr {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct && et.PkgPath() != "time" {
				leafKeys(et, key, out)
			} else {
				out[key] = true
			}
		default:
			out[key] = true
		}
	}
}

// unclaimedKeys computes leaves(root) minus claimed, and fails t for any
// claimed key that does not correspond to a walked leaf (a claim for a
// removed/renamed knob is itself drift).
func unclaimedKeys(t *testing.T, root reflect.Type, claimed map[string]any) []string {
	t.Helper()
	leaves := map[string]bool{}
	leafKeys(root, "", leaves)
	for key, fn := range claimed {
		if !leaves[key] {
			t.Errorf("config liveness: RegisterReader(%q) claims a key that is not a leaf of the config struct — fix or remove the claim", key)
		}
		v := reflect.ValueOf(fn)
		if !v.IsValid() || v.Kind() != reflect.Func || v.IsNil() {
			t.Errorf("config liveness: claim for %q is not a non-nil function reference", key)
		}
	}
	var unclaimed []string
	for key := range leaves {
		if _, ok := claimed[key]; !ok {
			unclaimed = append(unclaimed, key)
		}
	}
	sort.Strings(unclaimed)
	return unclaimed
}

// allowlistEntries parses ci/allowlists/config.txt into entry->bead
// (non-permanent) and the set of permanent entries.
func allowlistEntries(t *testing.T, path string) (map[string]string, map[string]bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("config liveness: cannot open allowlist %s: %v", path, err)
	}
	defer f.Close()
	entries := map[string]string{}
	permanent := map[string]bool{}
	inPermanent := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "permanent:") {
				inPermanent = true
			}
			continue
		}
		if trimmed == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			t.Errorf("config liveness: malformed allowlist line (want entry\\tbead\\treason): %q", line)
			continue
		}
		if inPermanent && fields[1] == "permanent" {
			permanent[fields[0]] = true
			continue
		}
		entries[fields[0]] = fields[1]
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("config liveness: reading %s: %v", path, err)
	}
	return entries, permanent
}

// TestConfigKeyLivenessCanary is the mandatory canary: a fixture knob with no
// reader MUST be caught by the walk, and a claimed knob must pass. If either
// assertion fails the gate is a placebo and the whole test file is untrusted.
func TestConfigKeyLivenessCanary(t *testing.T) {
	type fixtureSub struct {
		Orphan string `toml:"orphan_knob"` // deliberately reader-less
	}
	type fixture struct {
		Wired string     `toml:"wired_knob"`
		Sub   fixtureSub `toml:"sub"`
		Skip  string     `toml:"-"`
	}
	claimed := map[string]any{"wired_knob": func() {}}
	unclaimed := unclaimedKeys(t, reflect.TypeOf(fixture{}), claimed)
	if len(unclaimed) != 1 || unclaimed[0] != "sub.orphan_knob" {
		t.Fatalf("CANARY FAILURE — walk did not isolate the reader-less fixture knob; got unclaimed=%v (gate would be a placebo)", unclaimed)
	}
}

func snapshotClaimedReaders() map[string]any {
	readerMu.Lock()
	defer readerMu.Unlock()
	claimed := make(map[string]any, len(readerRegistry))
	for k, v := range readerRegistry {
		claimed[k] = v
	}
	return claimed
}

// TestConfigKeyLiveness is the G2 gate proper.
func TestConfigKeyLiveness(t *testing.T) {
	claimed := snapshotClaimedReaders()

	unclaimed := unclaimedKeys(t, reflect.TypeOf(Config{}), claimed)
	unreadExternal := repoUnreadExternalFieldSet(t)

	allowPath := configAllowlistPath()
	entries, permanent := allowlistEntries(t, allowPath)

	if dump := os.Getenv("NTM_CONFIG_LIVENESS_DUMP"); dump != "" {
		var b strings.Builder
		for _, key := range unclaimed {
			b.WriteString(key + "\n")
		}
		if err := os.WriteFile(dump, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("dump: %v", err)
		}
	}

	unclaimedSet := map[string]bool{}
	for _, key := range unclaimed {
		unclaimedSet[key] = true
		if permanent[key] {
			continue
		}
		if _, ok := entries[key]; !ok {
			t.Errorf("UNCLAIMED\t%s", key)
		}
	}
	var stale []string
	for entry := range entries {
		if !unclaimedSet[entry] && !unreadExternal[entry] {
			stale = append(stale, entry)
		}
	}
	sort.Strings(stale)
	for _, entry := range stale {
		t.Errorf("STALE\t%s\t%s", entry, entries[entry])
	}
	if t.Failed() {
		t.Log("config-key liveness ratchet (bd-ws0-guards-klz98.3):")
		t.Log("  UNCLAIMED = parsed key with no config.RegisterReader claim and no ci/allowlists/config.txt line.")
		t.Log("    Waiver protocol is bead-first: open a bead, then add '<key>\\t<bead-id>\\t<reason>'.")
		t.Log("    The real fix is a claim: config.RegisterReader(key, realReaderFunc) from the consuming package.")
		t.Log("  STALE = allowlist line whose key is now both claimed and externally read (or no longer parsed) — delete it in this PR.")
	}
}

func configAllowlistPath() string {
	if override := strings.TrimSpace(os.Getenv("NTM_CONFIG_LIVENESS_ALLOWLIST")); override != "" {
		return override
	}
	return filepath.Join("..", "..", "ci", "allowlists", "config.txt")
}

// unreadFieldDiagnostic is the structured diagnostic emitted when a TOML leaf
// field on Config has zero genuine external runtime reads.
type unreadFieldDiagnostic struct {
	UnreadTomlKey string `json:"unread_toml_key"`
	StructPath    string `json:"struct_path"`
	DeclaredAt    string `json:"declared_at"`
}

func formatUnreadFieldDiagnostic(prefix string, meta leafFieldMeta) string {
	diag := unreadFieldDiagnostic{
		UnreadTomlKey: meta.TomlKey,
		StructPath:    meta.StructPath,
		DeclaredAt:    meta.DeclaredAt,
	}
	encoded, _ := json.Marshal(diag)
	return fmt.Sprintf("%s\t%s\t%s", prefix, meta.TomlKey, string(encoded))
}

var (
	repoReadScanOnce   sync.Once
	repoLeafMetasCache []leafFieldMeta
	repoUnreadSetCache map[string]bool
	repoScanErrCache   error
)

func runCachedRepoExternalReadScan(t *testing.T) ([]leafFieldMeta, map[string]bool) {
	t.Helper()
	repoReadScanOnce.Do(func() {
		repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			repoScanErrCache = err
			return
		}
		repoLeafMetasCache, repoUnreadSetCache, repoScanErrCache = analyzeConfigExternalFieldReads(
			repoRoot,
			reflect.TypeOf(Config{}),
			"internal/config",
			"Config",
		)
	})
	if repoScanErrCache != nil {
		t.Fatalf("config external field-read AST scan failed: %v", repoScanErrCache)
	}
	return repoLeafMetasCache, repoUnreadSetCache
}

func repoUnreadExternalFieldSet(t *testing.T) map[string]bool {
	t.Helper()
	_, unread := runCachedRepoExternalReadScan(t)
	return unread
}

// TestConfigKeyLivenessExternalFieldReads upgrades WS0-G2 so a ceremonial
// RegisterReader(key, fn) claim or dead adapter copy cannot mask an unread
// Config struct field. Every walked TOML leaf field on Config must have >= 1
// genuine reachable external runtime read outside internal/config self-mgmt,
// *_test.go, and liveness_claims*.go — otherwise `permanent` is FORBIDDEN and
// the key must carry an OPEN implementation bead in ci/allowlists/config.txt.
func TestConfigKeyLivenessExternalFieldReads(t *testing.T) {
	metas, origUnreadSet := runCachedRepoExternalReadScan(t)
	unreadSet := origUnreadSet
	if forced := strings.TrimSpace(os.Getenv("NTM_CONFIG_LIVENESS_SELFTEST_FORCE_UNREAD")); forced != "" {
		unreadSet = make(map[string]bool, len(origUnreadSet)+1)
		for k, v := range origUnreadSet {
			unreadSet[k] = v
		}
		unreadSet[forced] = true
	}
	allowPath := configAllowlistPath()
	entries, permanent := allowlistEntries(t, allowPath)

	claimed := snapshotClaimedReaders()

	for _, meta := range metas {
		if !unreadSet[meta.TomlKey] {
			continue
		}
		if permanent[meta.TomlKey] {
			t.Errorf("%s (permanent is FORBIDDEN for fields with 0 external runtime reads; assign an open bead)",
				formatUnreadFieldDiagnostic("UNREAD_PERMANENT_FORBIDDEN", meta))
			continue
		}
		if _, ok := entries[meta.TomlKey]; !ok {
			t.Errorf("%s", formatUnreadFieldDiagnostic("UNREAD_FIELD", meta))
		}
	}

	var stale []string
	for entry := range entries {
		if _, isClaimed := claimed[entry]; isClaimed && !unreadSet[entry] {
			stale = append(stale, entry)
		}
	}
	sort.Strings(stale)
	for _, entry := range stale {
		t.Errorf("STALE_EXTERNAL_READ\t%s\t%s", entry, entries[entry])
	}

	t.Logf("config external field-read AST scan walked %d leaf fields (%d live external runtime reads, %d allowlisted unread)",
		len(metas), len(metas)-len(unreadSet), len(unreadSet))
}

// TestConfigKeyLivenessExternalFieldReadsCanary proves that a synthetic struct
// field that HAS a RegisterReader claim + Default*/Validate* self-references in
// internal/config + a pure copy into an unread target struct field + ZERO real
// external runtime reads FAILS with structured diagnostic fields
// (`unread_toml_key`, `struct_path`, `declared_at`).
func TestConfigKeyLivenessExternalFieldReadsCanary(t *testing.T) {
	tmpRoot := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		full := filepath.Join(tmpRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile("go.mod", "module github.com/Dicklesworthstone/ntm\n\ngo 1.26.3\n")
	writeFile("internal/config/synthetic.go", `package config

type SyntheticDeadSection struct {
	PhantomKnob int `+"`toml:\"phantom_knob\"`"+`
}

type SyntheticConfig struct {
	WiredKnob   string               `+"`toml:\"wired_knob\"`"+`
	DeadSection SyntheticDeadSection `+"`toml:\"dead_section\"`"+`
}

func DefaultSynthetic() *SyntheticConfig {
	return &SyntheticConfig{
		WiredKnob: "live",
		DeadSection: SyntheticDeadSection{
			PhantomKnob: 42,
		},
	}
}

func ValidateSynthetic(cfg *SyntheticConfig) bool {
	return cfg.DeadSection.PhantomKnob >= 0 && cfg.WiredKnob != ""
}
`)
	// Ceremonial claim file (must be ignored by external field-read verifier).
	writeFile("internal/consumer/liveness_claims.go", `package consumer

func init() {
	_ = "dead_section.phantom_knob"
}
`)
	// Consumer copies PhantomKnob into an unread TargetOpts field while genuinely
	// consuming WiredKnob at runtime.
	writeFile("internal/consumer/use.go", `package consumer

import (
	"fmt"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

type TargetOpts struct {
	Phantom int
}

func applyOverrides(dst *TargetOpts, cfg *config.SyntheticConfig) {
	if cfg.DeadSection.PhantomKnob > 0 {
		dst.Phantom = cfg.DeadSection.PhantomKnob
	}
}

func Run(cfg *config.SyntheticConfig) string {
	var opts TargetOpts
	applyOverrides(&opts, cfg)
	if cfg.WiredKnob != "" {
		return fmt.Sprintf("mode=%s", cfg.WiredKnob)
	}
	return "default"
}
`)
	writeFile("cmd/ntm/main.go", `package main

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/consumer"
)

func main() {
	cfg := config.DefaultSynthetic()
	_ = config.ValidateSynthetic(cfg)
	_ = consumer.Run(cfg)
}
`)

	type SyntheticDeadSection struct {
		PhantomKnob int `toml:"phantom_knob"`
	}
	type SyntheticConfig struct {
		WiredKnob   string               `toml:"wired_knob"`
		DeadSection SyntheticDeadSection `toml:"dead_section"`
	}

	// Even with a non-nil RegisterReader claim, the external AST field-read check must catch PhantomKnob.
	claimed := map[string]any{
		"wired_knob":                func() {},
		"dead_section.phantom_knob": func() {},
	}
	if unclaimed := unclaimedKeys(t, reflect.TypeOf(SyntheticConfig{}), claimed); len(unclaimed) != 0 {
		t.Fatalf("canary setup broken: expected 0 unclaimed keys with ceremonial RegisterReader, got %v", unclaimed)
	}

	metas, unreadSet, err := analyzeConfigExternalFieldReads(
		tmpRoot,
		reflect.TypeOf(SyntheticConfig{}),
		"internal/config",
		"SyntheticConfig",
	)
	if err != nil {
		t.Fatalf("canary analyzeConfigExternalFieldReads error: %v", err)
	}
	if unreadSet["wired_knob"] {
		t.Fatalf("CANARY FAILURE — wired_knob was falsely flagged as unread")
	}
	if !unreadSet["dead_section.phantom_knob"] {
		t.Fatalf("CANARY FAILURE — dead_section.phantom_knob with ceremonial RegisterReader + dead copy was NOT caught")
	}

	var diagLine string
	for _, m := range metas {
		if m.TomlKey == "dead_section.phantom_knob" {
			diagLine = formatUnreadFieldDiagnostic("UNREAD_FIELD", m)
			break
		}
	}
	for _, needle := range []string{
		`"unread_toml_key":"dead_section.phantom_knob"`,
		`"struct_path":"SyntheticConfig.DeadSection.PhantomKnob"`,
		`"declared_at":"internal/config/synthetic.go:4"`,
	} {
		if !strings.Contains(diagLine, needle) {
			t.Fatalf("CANARY FAILURE — diagnostic missing %s:\n%s", needle, diagLine)
		}
	}
}

// =============================================================================
// AST External Struct-Field Read Analyzer (WS0-G2 upgrade)
// =============================================================================

const ntmModulePrefix = "github.com/Dicklesworthstone/ntm/"

type structTypeKey struct {
	PkgRel string
	Name   string
}

type structFieldKey struct {
	PkgRel string
	Struct string
	Field  string
}

type rootedPathKey struct {
	RootPkg    string
	RootStruct string
	Path       string // dot-joined exported/unexported Go field names from RootStruct
}

type leafFieldMeta struct {
	TomlKey     string
	StructPath  string // e.g. "Config.Ensemble.Synthesis.MinConfidence"
	RootedChain string // e.g. "Ensemble.Synthesis.MinConfidence"
	FieldChain  []structFieldKey
	LeafField   structFieldKey
	DeclaredAt  string
}

type astFieldInfo struct {
	TypePkg     string
	TypeName    string
	IsComposite bool // slice, array, or map whose element type is TypePkg.TypeName
	TomlName    string
	DeclaredAt  string
}

type astStructInfo struct {
	Fields   map[string]astFieldInfo
	Embedded []structTypeKey
}

type funcKey struct {
	PkgRel string
	Recv   string // empty for package-level functions
	Name   string
}

type parsedFunc struct {
	Key        funcKey
	FileRel    string
	Decl       *ast.FuncDecl
	Imports    map[string]string // import alias -> pkgRel
	UsesTOML   bool
	ParamTypes []struct {
		Names      []string
		Type       structTypeKey
		IsVariadic bool
	}
	RecvNames   []string
	RecvType    structTypeKey
	ReturnTypes []structTypeKey
}

type repoASTIndex struct {
	Structs      map[structTypeKey]*astStructInfo
	TypeAliases  map[structTypeKey]structTypeKey
	PkgVars      map[string]map[string]structTypeKey // pkgRel -> varName -> type
	Funcs        map[funcKey][]*parsedFunc           // slice preserves build-tag variants
	FuncsByName  map[string][]*parsedFunc            // Name -> funcs (for interface/unresolved fallback)
	Inits        []*parsedFunc
	Mains        []*parsedFunc
	TomlDecoders map[string]structFieldKey // dotted toml path -> external struct leaf field
}

func reflectPkgToRel(pkgPath, defaultPkgRel string) string {
	if pkgPath == "" {
		return defaultPkgRel
	}
	if strings.HasPrefix(pkgPath, ntmModulePrefix) {
		return strings.TrimPrefix(pkgPath, ntmModulePrefix)
	}
	return pkgPath
}

func collectLeafFieldMeta(
	t reflect.Type,
	defaultPkgRel string,
	rootStructName string,
	tomlPrefix string,
	structPathPrefix string,
	chainPrefix []structFieldKey,
	idx *repoASTIndex,
	out *[]leafFieldMeta,
) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.PkgPath() == "time" {
		return
	}
	ownerPkg := reflectPkgToRel(t.PkgPath(), defaultPkgRel)
	ownerName := t.Name()
	if ownerName == "" {
		ownerName = rootStructName
	}

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("toml")
		if comma := strings.Index(tag, ","); comma >= 0 {
			tag = tag[:comma]
		}
		if tag == "-" {
			continue
		}
		tomlName := tag
		if tomlName == "" {
			tomlName = strings.ToLower(f.Name)
		}
		tomlKey := tomlName
		if tomlPrefix != "" {
			tomlKey = tomlPrefix + "." + tomlName
		}
		structPath := structPathPrefix + "." + f.Name
		hop := structFieldKey{PkgRel: ownerPkg, Struct: ownerName, Field: f.Name}
		nextChain := append(append([]structFieldKey(nil), chainPrefix...), hop)

		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		recurse := false
		var elemType reflect.Type
		switch ft.Kind() {
		case reflect.Struct:
			if ft != reflect.TypeOf(time.Time{}) && ft.PkgPath() != "time" {
				recurse = true
				elemType = ft
			}
		case reflect.Slice, reflect.Array:
			et := ft.Elem()
			for et.Kind() == reflect.Ptr {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct && et != reflect.TypeOf(time.Time{}) && et.PkgPath() != "time" {
				recurse = true
				elemType = et
			}
		}
		if recurse {
			collectLeafFieldMeta(elemType, defaultPkgRel, elemType.Name(), tomlKey, structPath, nextChain, idx, out)
			continue
		}

		declAt := ""
		if st := idx.Structs[structTypeKey{PkgRel: ownerPkg, Name: ownerName}]; st != nil {
			if fi, ok := st.Fields[f.Name]; ok {
				declAt = fi.DeclaredAt
			}
		}
		parts := strings.SplitN(structPath, ".", 2)
		rooted := ""
		if len(parts) == 2 {
			rooted = parts[1]
		}
		*out = append(*out, leafFieldMeta{
			TomlKey:     tomlKey,
			StructPath:  structPath,
			RootedChain: rooted,
			FieldChain:  nextChain,
			LeafField:   hop,
			DeclaredAt:  declAt,
		})
	}
}

func parseRepoASTIndex(repoRoot string) (*repoASTIndex, error) {
	idx := &repoASTIndex{
		Structs:      map[structTypeKey]*astStructInfo{},
		TypeAliases:  map[structTypeKey]structTypeKey{},
		PkgVars:      map[string]map[string]structTypeKey{},
		Funcs:        map[funcKey][]*parsedFunc{},
		FuncsByName:  map[string][]*parsedFunc{},
		TomlDecoders: map[string]structFieldKey{},
	}

	type parsedFileEntry struct {
		pkgRel  string
		fileRel string
		astFile *ast.File
		imports map[string]string
		useTOML bool
	}
	var files []parsedFileEntry
	fset := token.NewFileSet()

	for _, topDir := range []string{"cmd", "internal"} {
		absTop := filepath.Join(repoRoot, topDir)
		if _, err := os.Stat(absTop); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(absTop, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			if strings.HasPrefix(name, "liveness_claims") {
				return nil
			}
			af, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", path, parseErr)
			}
			relFile, _ := filepath.Rel(repoRoot, path)
			relFile = filepath.ToSlash(relFile)
			pkgRel := filepath.ToSlash(filepath.Dir(relFile))

			imports := map[string]string{}
			useTOML := false
			for _, imp := range af.Imports {
				impPath := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(impPath, "BurntSushi/toml") {
					useTOML = true
				}
				if !strings.HasPrefix(impPath, ntmModulePrefix) {
					continue
				}
				targetRel := strings.TrimPrefix(impPath, ntmModulePrefix)
				alias := filepath.Base(targetRel)
				if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." {
					alias = imp.Name.Name
				}
				imports[alias] = targetRel
			}
			files = append(files, parsedFileEntry{
				pkgRel:  pkgRel,
				fileRel: relFile,
				astFile: af,
				imports: imports,
				useTOML: useTOML,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// Pass 1: collect structs, type aliases, package vars, and function signatures.
	initSeq := 0
	pkgUsesTOML := map[string]bool{}
	for _, fe := range files {
		if fe.useTOML {
			pkgUsesTOML[fe.pkgRel] = true
		}
		if idx.PkgVars[fe.pkgRel] == nil {
			idx.PkgVars[fe.pkgRel] = map[string]structTypeKey{}
		}
		for _, decl := range fe.astFile.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if st, ok := s.Type.(*ast.StructType); ok {
							info := &astStructInfo{Fields: map[string]astFieldInfo{}}
							if st.Fields != nil {
								for _, field := range st.Fields.List {
									tRef, isComp := resolveASTTypeExpr(field.Type, fe.pkgRel, fe.imports)
									tomlTag := parseASTTomlTag(field.Tag)
									pos := fset.Position(field.Pos())
									declAt := fmt.Sprintf("%s:%d", fe.fileRel, pos.Line)
									if len(field.Names) == 0 {
										if tRef.Name != "" {
											info.Embedded = append(info.Embedded, tRef)
										}
										continue
									}
									for _, ident := range field.Names {
										tName := tomlTag
										if tName == "" {
											tName = strings.ToLower(ident.Name)
										}
										info.Fields[ident.Name] = astFieldInfo{
											TypePkg:     tRef.PkgRel,
											TypeName:    tRef.Name,
											IsComposite: isComp,
											TomlName:    tName,
											DeclaredAt:  declAt,
										}
									}
								}
							}
							idx.Structs[structTypeKey{PkgRel: fe.pkgRel, Name: s.Name.Name}] = info
						} else {
							target, _ := resolveASTTypeExpr(s.Type, fe.pkgRel, fe.imports)
							if target.Name != "" {
								idx.TypeAliases[structTypeKey{PkgRel: fe.pkgRel, Name: s.Name.Name}] = target
							}
						}
					case *ast.ValueSpec:
						tRef, _ := resolveASTTypeExpr(s.Type, fe.pkgRel, fe.imports)
						for i, n := range s.Names {
							vType := tRef
							if vType.Name == "" && i < len(s.Values) {
								vType = resolvePkgVarInitType(s.Values[i], fe.pkgRel, fe.imports)
							}
							if vType.Name != "" {
								idx.PkgVars[fe.pkgRel][n.Name] = vType
							}
						}
						if len(s.Values) > 0 {
							var stmts []ast.Stmt
							for _, val := range s.Values {
								stmts = append(stmts, &ast.ExprStmt{X: val})
							}
							fnName := fmt.Sprintf("pkgvar#%d", initSeq)
							initSeq++
							pf := &parsedFunc{
								Key:      funcKey{PkgRel: fe.pkgRel, Name: fnName},
								FileRel:  fe.fileRel,
								Decl:     &ast.FuncDecl{Name: ast.NewIdent(fnName), Type: &ast.FuncType{}, Body: &ast.BlockStmt{List: stmts}},
								Imports:  fe.imports,
								UsesTOML: fe.useTOML,
							}
							idx.Funcs[pf.Key] = append(idx.Funcs[pf.Key], pf)
							idx.Inits = append(idx.Inits, pf)
						}
					}
				}
			case *ast.FuncDecl:
				pf := &parsedFunc{
					FileRel:  fe.fileRel,
					Decl:     d,
					Imports:  fe.imports,
					UsesTOML: fe.useTOML,
				}
				recvName := ""
				if d.Recv != nil && len(d.Recv.List) > 0 {
					rt, _ := resolveASTTypeExpr(d.Recv.List[0].Type, fe.pkgRel, fe.imports)
					recvName = rt.Name
					pf.RecvType = rt
					for _, rn := range d.Recv.List[0].Names {
						pf.RecvNames = append(pf.RecvNames, rn.Name)
					}
				}
				if d.Type.Params != nil {
					for _, p := range d.Type.Params.List {
						pExpr := p.Type
						isVar := false
						if ell, ok := pExpr.(*ast.Ellipsis); ok {
							isVar = true
							pExpr = ell.Elt
						}
						pt, _ := resolveASTTypeExpr(pExpr, fe.pkgRel, fe.imports)
						var names []string
						for _, pn := range p.Names {
							names = append(names, pn.Name)
						}
						pf.ParamTypes = append(pf.ParamTypes, struct {
							Names      []string
							Type       structTypeKey
							IsVariadic bool
						}{Names: names, Type: pt, IsVariadic: isVar})
					}
				}
				if d.Type.Results != nil {
					for _, res := range d.Type.Results.List {
						rt, _ := resolveASTTypeExpr(res.Type, fe.pkgRel, fe.imports)
						count := len(res.Names)
						if count == 0 {
							count = 1
						}
						for range count {
							pf.ReturnTypes = append(pf.ReturnTypes, rt)
						}
					}
				}
				fnName := d.Name.Name
				if recvName == "" && fnName == "init" {
					fnName = fmt.Sprintf("init#%d", initSeq)
					initSeq++
					pf.Key = funcKey{PkgRel: fe.pkgRel, Name: fnName}
					idx.Funcs[pf.Key] = append(idx.Funcs[pf.Key], pf)
					idx.Inits = append(idx.Inits, pf)
					continue
				}
				pf.Key = funcKey{PkgRel: fe.pkgRel, Recv: recvName, Name: fnName}
				idx.Funcs[pf.Key] = append(idx.Funcs[pf.Key], pf)
				idx.FuncsByName[fnName] = append(idx.FuncsByName[fnName], pf)
				if recvName == "" && fnName == "main" && strings.HasPrefix(fe.pkgRel, "cmd/") {
					idx.Mains = append(idx.Mains, pf)
				}
			}
		}
	}

	// Index external TOML-decoded structs (e.g., hooks.CommandHooksConfig decoding [[command_hooks]]).
	for sk, st := range idx.Structs {
		if sk.PkgRel == "internal/config" || !pkgUsesTOML[sk.PkgRel] {
			continue
		}
		for _, fi := range st.Fields {
			if fi.TomlName == "" || fi.TomlName == "-" || fi.TypeName == "" {
				continue
			}
			elemKey := structTypeKey{PkgRel: fi.TypePkg, Name: fi.TypeName}
			if elemSt := idx.Structs[elemKey]; elemSt != nil {
				for childField, childInfo := range elemSt.Fields {
					if childInfo.TomlName == "" || childInfo.TomlName == "-" {
						continue
					}
					dotted := fi.TomlName + "." + childInfo.TomlName
					idx.TomlDecoders[dotted] = structFieldKey{
						PkgRel: elemKey.PkgRel,
						Struct: elemKey.Name,
						Field:  childField,
					}
				}
			}
		}
	}

	return idx, nil
}

func parseASTTomlTag(lit *ast.BasicLit) string {
	if lit == nil {
		return ""
	}
	raw := strings.Trim(lit.Value, "`")
	st := reflect.StructTag(raw)
	tag := st.Get("toml")
	if comma := strings.Index(tag, ","); comma >= 0 {
		tag = tag[:comma]
	}
	return tag
}

func resolveASTTypeExpr(expr ast.Expr, curPkgRel string, imports map[string]string) (structTypeKey, bool) {
	if expr == nil {
		return structTypeKey{}, false
	}
	switch e := expr.(type) {
	case *ast.StarExpr:
		return resolveASTTypeExpr(e.X, curPkgRel, imports)
	case *ast.ParenExpr:
		return resolveASTTypeExpr(e.X, curPkgRel, imports)
	case *ast.Ident:
		if isBuiltinGoType(e.Name) {
			return structTypeKey{}, false
		}
		return structTypeKey{PkgRel: curPkgRel, Name: e.Name}, false
	case *ast.SelectorExpr:
		if pkgIdent, ok := e.X.(*ast.Ident); ok {
			if targetPkg, found := imports[pkgIdent.Name]; found {
				return structTypeKey{PkgRel: targetPkg, Name: e.Sel.Name}, false
			}
		}
		return structTypeKey{}, false
	case *ast.ArrayType:
		elem, _ := resolveASTTypeExpr(e.Elt, curPkgRel, imports)
		return elem, elem.Name != ""
	case *ast.MapType:
		elem, _ := resolveASTTypeExpr(e.Value, curPkgRel, imports)
		return elem, elem.Name != ""
	default:
		return structTypeKey{}, false
	}
}

func isBuiltinGoType(name string) bool {
	switch name {
	case "bool", "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"byte", "rune", "float32", "float64", "complex64", "complex128",
		"error", "any":
		return true
	default:
		return false
	}
}

func (idx *repoASTIndex) canonicalType(tk structTypeKey) structTypeKey {
	for range 4 {
		next, ok := idx.TypeAliases[tk]
		if !ok {
			break
		}
		tk = next
	}
	return tk
}

func (idx *repoASTIndex) lookupField(tk structTypeKey, fieldName string) (structFieldKey, astFieldInfo, bool) {
	tk = idx.canonicalType(tk)
	visited := map[structTypeKey]bool{}
	var search func(cur structTypeKey) (structFieldKey, astFieldInfo, bool)
	search = func(cur structTypeKey) (structFieldKey, astFieldInfo, bool) {
		cur = idx.canonicalType(cur)
		if visited[cur] {
			return structFieldKey{}, astFieldInfo{}, false
		}
		visited[cur] = true
		st := idx.Structs[cur]
		if st == nil {
			return structFieldKey{}, astFieldInfo{}, false
		}
		if fi, ok := st.Fields[fieldName]; ok {
			return structFieldKey{PkgRel: cur.PkgRel, Struct: cur.Name, Field: fieldName}, fi, true
		}
		for _, emb := range st.Embedded {
			if sfk, fi, ok := search(emb); ok {
				return sfk, fi, true
			}
		}
		return structFieldKey{}, astFieldInfo{}, false
	}
	return search(tk)
}

func resolvePkgVarInitType(expr ast.Expr, curPkgRel string, imports map[string]string) structTypeKey {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return resolvePkgVarInitType(e.X, curPkgRel, imports)
		}
	case *ast.CompositeLit:
		tk, _ := resolveASTTypeExpr(e.Type, curPkgRel, imports)
		return tk
	}
	return structTypeKey{}
}

func (idx *repoASTIndex) lookupMethods(tk structTypeKey, methodName string) []*parsedFunc {
	tk = idx.canonicalType(tk)
	visited := map[structTypeKey]bool{}
	var out []*parsedFunc
	var search func(cur structTypeKey)
	search = func(cur structTypeKey) {
		cur = idx.canonicalType(cur)
		if visited[cur] {
			return
		}
		visited[cur] = true
		if pfs := idx.Funcs[funcKey{PkgRel: cur.PkgRel, Recv: cur.Name, Name: methodName}]; len(pfs) > 0 {
			out = append(out, pfs...)
			return
		}
		if st := idx.Structs[cur]; st != nil {
			for _, emb := range st.Embedded {
				search(emb)
			}
		}
	}
	search(tk)
	return out
}

type varOrigin struct {
	Type      structTypeKey
	IsElem    bool
	RootType  structTypeKey // top-level container struct if derived via selector chain
	RootChain string        // dot-joined field path from RootType
}

func joinRootedPath(prefix, suffix string) string {
	if prefix == "" {
		return suffix
	}
	if suffix == "" {
		return prefix
	}
	return prefix + "." + suffix
}

func inferFuncVarOrigins(idx *repoASTIndex, pf *parsedFunc, base map[string]varOrigin) map[string]varOrigin {
	origins := map[string]varOrigin{}
	for k, v := range base {
		origins[k] = v
	}
	if pkgVars := idx.PkgVars[pf.Key.PkgRel]; pkgVars != nil {
		for name, tk := range pkgVars {
			origins[name] = varOrigin{Type: tk, RootType: tk}
		}
	}
	if pf.RecvType.Name != "" {
		for _, rn := range pf.RecvNames {
			origins[rn] = varOrigin{Type: pf.RecvType, RootType: pf.RecvType}
		}
	}
	for _, p := range pf.ParamTypes {
		if p.Type.Name == "" {
			continue
		}
		for _, pn := range p.Names {
			origins[pn] = varOrigin{Type: p.Type, RootType: p.Type}
		}
	}
	if pf.Decl.Body == nil {
		return origins
	}

	// Fixed-point local inference (3 passes handles chained local assignments).
	for range 3 {
		ast.Inspect(pf.Decl.Body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.FuncLit:
				return false // handled with nested scope when visiting closures
			case *ast.DeclStmt:
				gd, ok := s.Decl.(*ast.GenDecl)
				if !ok {
					return true
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					explicitType, _ := resolveASTTypeExpr(vs.Type, pf.Key.PkgRel, pf.Imports)
					if explicitType.Name == "" && len(vs.Values) == 1 && len(vs.Names) > 1 {
						if call, isCall := vs.Values[0].(*ast.CallExpr); isCall {
							for _, callee := range resolveCalleeFuncs(idx, pf, origins, call.Fun) {
								for i, ident := range vs.Names {
									if ident.Name != "_" && i < len(callee.ReturnTypes) && callee.ReturnTypes[i].Name != "" {
										rt := callee.ReturnTypes[i]
										origins[ident.Name] = varOrigin{Type: rt, RootType: rt}
									}
								}
							}
							continue
						}
					}
					for i, ident := range vs.Names {
						if explicitType.Name != "" {
							origins[ident.Name] = varOrigin{Type: explicitType, RootType: explicitType}
						} else if i < len(vs.Values) {
							if vo, ok := resolveExprOrigin(idx, pf, origins, vs.Values[i]); ok {
								origins[ident.Name] = vo
							}
						}
					}
				}
			case *ast.AssignStmt:
				if len(s.Lhs) == len(s.Rhs) {
					for i, lhs := range s.Lhs {
						ident, ok := lhs.(*ast.Ident)
						if !ok || ident.Name == "_" {
							continue
						}
						if vo, ok := resolveExprOrigin(idx, pf, origins, s.Rhs[i]); ok {
							origins[ident.Name] = vo
						}
					}
				} else if len(s.Rhs) == 1 && len(s.Lhs) >= 1 {
					if call, isCall := s.Rhs[0].(*ast.CallExpr); isCall {
						matchedTuple := false
						for _, callee := range resolveCalleeFuncs(idx, pf, origins, call.Fun) {
							if len(callee.ReturnTypes) > 0 {
								matchedTuple = true
								for i, lhs := range s.Lhs {
									ident, ok := lhs.(*ast.Ident)
									if !ok || ident.Name == "_" || i >= len(callee.ReturnTypes) {
										continue
									}
									rt := callee.ReturnTypes[i]
									if rt.Name != "" {
										origins[ident.Name] = varOrigin{Type: rt, RootType: rt}
									}
								}
							}
						}
						if matchedTuple {
							return true
						}
					}
					if ident, ok := s.Lhs[0].(*ast.Ident); ok && ident.Name != "_" {
						if vo, ok := resolveExprOrigin(idx, pf, origins, s.Rhs[0]); ok {
							origins[ident.Name] = vo
						}
					}
				}
			case *ast.RangeStmt:
				if s.Value != nil {
					if valIdent, ok := s.Value.(*ast.Ident); ok && valIdent.Name != "_" {
						if vo, ok := resolveExprOrigin(idx, pf, origins, s.X); ok && vo.Type.Name != "" {
							origins[valIdent.Name] = varOrigin{
								Type:      vo.Type,
								RootType:  vo.RootType,
								RootChain: vo.RootChain,
							}
						}
					}
				}
			}
			return true
		})
	}
	return origins
}

func resolveExprOrigin(idx *repoASTIndex, pf *parsedFunc, origins map[string]varOrigin, expr ast.Expr) (varOrigin, bool) {
	if expr == nil {
		return varOrigin{}, false
	}
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return resolveExprOrigin(idx, pf, origins, e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return resolveExprOrigin(idx, pf, origins, e.X)
		}
	case *ast.StarExpr:
		return resolveExprOrigin(idx, pf, origins, e.X)
	case *ast.IndexExpr:
		vo, ok := resolveExprOrigin(idx, pf, origins, e.X)
		if ok && vo.Type.Name != "" {
			vo.IsElem = false
			return vo, true
		}
	case *ast.CompositeLit:
		tk, _ := resolveASTTypeExpr(e.Type, pf.Key.PkgRel, pf.Imports)
		if tk.Name != "" {
			return varOrigin{Type: tk, RootType: tk}, true
		}
	case *ast.TypeAssertExpr:
		tk, _ := resolveASTTypeExpr(e.Type, pf.Key.PkgRel, pf.Imports)
		if tk.Name != "" {
			return varOrigin{Type: tk, RootType: tk}, true
		}
	case *ast.Ident:
		if vo, ok := origins[e.Name]; ok {
			return vo, true
		}
	case *ast.SelectorExpr:
		baseVO, ok := resolveExprOrigin(idx, pf, origins, e.X)
		if ok && baseVO.Type.Name != "" {
			if _, fi, found := idx.lookupField(baseVO.Type, e.Sel.Name); found {
				chain := joinRootedPath(baseVO.RootChain, e.Sel.Name)
				root := baseVO.RootType
				if root.Name == "" {
					root = baseVO.Type
				}
				if fi.TypeName != "" {
					return varOrigin{
						Type:      structTypeKey{PkgRel: fi.TypePkg, Name: fi.TypeName},
						IsElem:    fi.IsComposite,
						RootType:  root,
						RootChain: chain,
					}, true
				}
				return varOrigin{
					RootType:  root,
					RootChain: chain,
				}, true
			}
		}
	case *ast.CallExpr:
		for _, callee := range resolveCalleeFuncs(idx, pf, origins, e.Fun) {
			if len(callee.ReturnTypes) > 0 && callee.ReturnTypes[0].Name != "" {
				rt := callee.ReturnTypes[0]
				return varOrigin{Type: rt, RootType: rt}, true
			}
		}
	}
	return varOrigin{}, false
}

func resolveCalleeFuncs(idx *repoASTIndex, pf *parsedFunc, origins map[string]varOrigin, funExpr ast.Expr) []*parsedFunc {
	switch fn := funExpr.(type) {
	case *ast.Ident:
		return idx.Funcs[funcKey{PkgRel: pf.Key.PkgRel, Name: fn.Name}]
	case *ast.SelectorExpr:
		if pkgIdent, ok := fn.X.(*ast.Ident); ok {
			if targetPkg, isImport := pf.Imports[pkgIdent.Name]; isImport {
				return idx.Funcs[funcKey{PkgRel: targetPkg, Name: fn.Sel.Name}]
			}
		}
		if recvVO, ok := resolveExprOrigin(idx, pf, origins, fn.X); ok && recvVO.Type.Name != "" {
			return idx.lookupMethods(recvVO.Type, fn.Sel.Name)
		}
	}
	return nil
}

func isConfigSelfManagementFunc(configPkgRel string, fk funcKey) bool {
	if fk.PkgRel != configPkgRel {
		return false
	}
	name := fk.Name
	for _, prefix := range []string{
		"Default", "default",
		"Load", "load",
		"Merge", "merge",
		"Validate", "validate",
		"Print", "print",
		"Format", "format",
		"Write", "write",
		"Save", "save",
		"Create", "create",
		"Reset", "reset",
		"Parse", "parse",
		"GetValue", "Diff",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func isCeremonialValidatorOrDryRunFunc(fk funcKey, fileRel string) bool {
	if fileRel == "internal/cli/validate.go" {
		return true
	}
	name := fk.Name
	if strings.Contains(strings.ToLower(name), "dryrun") {
		return true
	}
	for _, prefix := range []string{
		"Validate", "validate",
		"newConfigShow", "newConfigDiff", "newConfigGet",
		"runConfigShow", "runConfigDiff", "runConfigGet",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func funcMatchesCallParams(
	idx *repoASTIndex,
	caller *parsedFunc,
	origins map[string]varOrigin,
	cand *parsedFunc,
	args []ast.Expr,
	hasEllipsisArg bool,
) bool {
	var flatParams []struct {
		Type       structTypeKey
		IsVariadic bool
	}
	isVariadic := false
	for _, pg := range cand.ParamTypes {
		n := len(pg.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			flatParams = append(flatParams, struct {
				Type       structTypeKey
				IsVariadic bool
			}{Type: pg.Type, IsVariadic: pg.IsVariadic})
		}
		if pg.IsVariadic {
			isVariadic = true
		}
	}
	totalParams := len(flatParams)
	if isVariadic {
		if hasEllipsisArg {
			if len(args) < totalParams {
				return false
			}
		} else if len(args) < totalParams-1 {
			return false
		}
	} else if len(args) != totalParams {
		return false
	}

	for i, arg := range args {
		if i >= len(flatParams) {
			break
		}
		if flatParams[i].IsVariadic {
			break
		}
		expected := idx.canonicalType(flatParams[i].Type)
		if expected.Name == "" || idx.Structs[expected] == nil {
			continue
		}
		if _, isBasicLit := arg.(*ast.BasicLit); isBasicLit {
			return false
		}
		if argVO, ok := resolveExprOrigin(idx, caller, origins, arg); ok && argVO.Type.Name != "" {
			if idx.canonicalType(argVO.Type) != expected {
				return false
			}
		}
	}
	return true
}

func computeReachableFuncs(idx *repoASTIndex) (map[*parsedFunc]bool, map[*parsedFunc]bool) {
	reachable := map[*parsedFunc]bool{}
	calledExternally := map[*parsedFunc]bool{} // functions in internal/config called from outside internal/config
	liveTypes := map[structTypeKey]bool{}
	var queue []*parsedFunc

	enqueue := func(pf *parsedFunc) {
		if pf == nil || reachable[pf] {
			return
		}
		reachable[pf] = true
		if pf.RecvType.Name != "" {
			liveTypes[idx.canonicalType(pf.RecvType)] = true
		}
		for _, rt := range pf.ReturnTypes {
			if rt.Name != "" {
				liveTypes[idx.canonicalType(rt)] = true
			}
		}
		queue = append(queue, pf)
	}

	// Imported packages reachable from cmd/*
	importedPkgs := map[string]bool{}
	var pkgQueue []string
	for _, m := range idx.Mains {
		importedPkgs[m.Key.PkgRel] = true
		pkgQueue = append(pkgQueue, m.Key.PkgRel)
		enqueue(m)
	}
	pkgImports := map[string]map[string]bool{}
	for _, pfs := range idx.Funcs {
		for _, pf := range pfs {
			if pkgImports[pf.Key.PkgRel] == nil {
				pkgImports[pf.Key.PkgRel] = map[string]bool{}
			}
			for _, imp := range pf.Imports {
				pkgImports[pf.Key.PkgRel][imp] = true
			}
		}
	}
	for len(pkgQueue) > 0 {
		cur := pkgQueue[0]
		pkgQueue = pkgQueue[1:]
		for imp := range pkgImports[cur] {
			if !importedPkgs[imp] {
				importedPkgs[imp] = true
				pkgQueue = append(pkgQueue, imp)
			}
		}
	}
	for _, initFn := range idx.Inits {
		if importedPkgs[initFn.Key.PkgRel] {
			enqueue(initFn)
		}
	}

	var walkBlock func(pf *parsedFunc, block *ast.BlockStmt, origins map[string]varOrigin)
	walkBlock = func(pf *parsedFunc, block *ast.BlockStmt, origins map[string]varOrigin) {
		if block == nil {
			return
		}
		ast.Inspect(block, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.FuncLit:
				nested := map[string]varOrigin{}
				for k, v := range origins {
					nested[k] = v
				}
				if e.Type.Params != nil {
					for _, p := range e.Type.Params.List {
						pt, _ := resolveASTTypeExpr(p.Type, pf.Key.PkgRel, pf.Imports)
						if pt.Name != "" {
							liveTypes[idx.canonicalType(pt)] = true
							for _, pn := range p.Names {
								nested[pn.Name] = varOrigin{Type: pt, RootType: pt}
							}
						}
					}
				}
				fakeFn := *pf
				fakeFn.Decl = &ast.FuncDecl{Name: pf.Decl.Name, Type: e.Type, Body: e.Body}
				nested = inferFuncVarOrigins(idx, &fakeFn, nested)
				for _, vo := range nested {
					if vo.Type.Name != "" {
						liveTypes[idx.canonicalType(vo.Type)] = true
					}
				}
				walkBlock(pf, e.Body, nested)
				return false
			case *ast.CompositeLit:
				tk, _ := resolveASTTypeExpr(e.Type, pf.Key.PkgRel, pf.Imports)
				if tk.Name != "" {
					liveTypes[idx.canonicalType(tk)] = true
				}
			case *ast.CallExpr:
				callees := resolveCalleeFuncs(idx, pf, origins, e.Fun)
				if len(callees) > 0 {
					for _, callee := range callees {
						if pf.Key.PkgRel != callee.Key.PkgRel {
							calledExternally[callee] = true
						}
						enqueue(callee)
					}
				} else if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
					// Bubbletea event-loop entrypoint `tea.NewProgram(model, ...)` invokes
					// Init/Update/View on the passed tea.Model implementation.
					if sel.Sel.Name == "NewProgram" && len(e.Args) >= 1 {
						if modelVO, ok := resolveExprOrigin(idx, pf, origins, e.Args[0]); ok && modelVO.Type.Name != "" {
							for _, mName := range []string{"Init", "Update", "View"} {
								for _, mFn := range idx.lookupMethods(modelVO.Type, mName) {
									enqueue(mFn)
								}
							}
						}
					}
					// Interface or unresolved receiver dispatch: skip imported package
					// qualifiers and require matching parameter arity.
					isExternalPkg := false
					if ident, ok := sel.X.(*ast.Ident); ok {
						if _, isImp := pf.Imports[ident.Name]; isImp {
							isExternalPkg = true
						} else if _, hasLocal := origins[ident.Name]; !hasLocal {
							isExternalPkg = true
						}
					}
					if !isExternalPkg {
						for _, cand := range idx.FuncsByName[sel.Sel.Name] {
							if cand.RecvType.Name != "" &&
								liveTypes[idx.canonicalType(cand.RecvType)] &&
								funcMatchesCallParams(idx, pf, origins, cand, e.Args, e.Ellipsis.IsValid()) {
								if pf.Key.PkgRel != cand.Key.PkgRel {
									calledExternally[cand] = true
								}
								enqueue(cand)
							}
						}
					}
				}
			case *ast.SelectorExpr:
				// Function/method value reference (e.g., RunE: pkg.Handler or obj.Method).
				if pkgIdent, ok := e.X.(*ast.Ident); ok {
					if targetPkg, isImport := pf.Imports[pkgIdent.Name]; isImport {
						for _, refFn := range idx.Funcs[funcKey{PkgRel: targetPkg, Name: e.Sel.Name}] {
							calledExternally[refFn] = true
							enqueue(refFn)
						}
					}
				} else if recvVO, ok := resolveExprOrigin(idx, pf, origins, e.X); ok && recvVO.Type.Name != "" {
					for _, refMethod := range idx.lookupMethods(recvVO.Type, e.Sel.Name) {
						if pf.Key.PkgRel != refMethod.Key.PkgRel {
							calledExternally[refMethod] = true
						}
						enqueue(refMethod)
					}
				}
			case *ast.Ident:
				for _, refFn := range idx.Funcs[funcKey{PkgRel: pf.Key.PkgRel, Name: e.Name}] {
					enqueue(refFn)
				}
			}
			return true
		})
	}

	// Run two passes so methods on newly discovered liveTypes wake up cleanly.
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			for pf := range reachable {
				queue = append(queue, pf)
			}
		}
		for len(queue) > 0 {
			pf := queue[0]
			queue = queue[1:]
			if pf == nil || pf.Decl.Body == nil {
				continue
			}
			origins := inferFuncVarOrigins(idx, pf, nil)
			for _, vo := range origins {
				if vo.Type.Name != "" {
					liveTypes[idx.canonicalType(vo.Type)] = true
				}
			}
			walkBlock(pf, pf.Decl.Body, origins)
		}
	}

	return reachable, calledExternally
}

func analyzeConfigExternalFieldReads(
	repoRoot string,
	configType reflect.Type,
	configPkgRel string,
	configStructName string,
) ([]leafFieldMeta, map[string]bool, error) {
	idx, err := parseRepoASTIndex(repoRoot)
	if err != nil {
		return nil, nil, err
	}

	var metas []leafFieldMeta
	collectLeafFieldMeta(configType, configPkgRel, configStructName, "", configStructName, nil, idx, &metas)
	sort.Slice(metas, func(i, j int) bool { return metas[i].TomlKey < metas[j].TomlKey })

	reachable, calledExternally := computeReachableFuncs(idx)

	// Map struct types belonging to Config's own schema tree so we know when a
	// direct read on `(declPkg, declStruct, Field)` is reading Config vs an
	// unrelated domain struct.
	configOwnedStructs := map[structTypeKey]int{}
	for _, m := range metas {
		for _, hop := range m.FieldChain {
			configOwnedStructs[structTypeKey{PkgRel: hop.PkgRel, Name: hop.Struct}]++
		}
	}

	// Track:
	// 1. directConsumedLeaf[structFieldKey]: leaf field directly consumed at runtime
	// 2. directConsumedRooted[rootedPathKey]: rooted path consumed at runtime
	// 3. copyEdges[rootedPathKey -> []rootedPathKey]: pure adapter copies between structs
	// 4. leafCopyTargets[structFieldKey -> []rootedPathKey]: adapter copies out of a Config leaf
	// 5. leafHasDirectRuntimeRead[structFieldKey]: true if a Config leaf has >=1 non-copy runtime read
	directConsumedLeaf := map[structFieldKey]bool{}
	directConsumedRooted := map[rootedPathKey]bool{}
	copyEdges := map[rootedPathKey][]rootedPathKey{}
	leafCopyTargets := map[structFieldKey][]rootedPathKey{}

	addCopyEdge := func(src, dst rootedPathKey) {
		if src == dst || dst.RootStruct == "" || dst.Path == "" {
			return
		}
		copyEdges[src] = append(copyEdges[src], dst)
	}

	// Analyze every reachable function body (and nested closures).
	for pf := range reachable {
		if pf == nil || pf.Decl.Body == nil {
			continue
		}
		fk := pf.Key
		if isCeremonialValidatorOrDryRunFunc(fk, pf.FileRel) {
			continue
		}
		inConfigSelfMgmt := isConfigSelfManagementFunc(configPkgRel, fk)
		if fk.PkgRel == configPkgRel && !inConfigSelfMgmt && !calledExternally[pf] {
			continue
		}

		origins := inferFuncVarOrigins(idx, pf, nil)
		scanBlockForFieldReads(
			idx,
			pf,
			pf.Decl.Body,
			origins,
			configPkgRel,
			configStructName,
			inConfigSelfMgmt,
			configOwnedStructs,
			directConsumedLeaf,
			directConsumedRooted,
			leafCopyTargets,
			addCopyEdge,
		)
	}

	// Propagate consumption backwards across copyEdges (fixed point).
	changed := true
	for changed {
		changed = false
		for src, dsts := range copyEdges {
			if directConsumedRooted[src] {
				continue
			}
			for _, dst := range dsts {
				if isRootedPathConsumed(dst, directConsumedRooted) {
					directConsumedRooted[src] = true
					changed = true
					break
				}
			}
		}
	}

	unread := map[string]bool{}
	for _, meta := range metas {
		live := false
		if directConsumedLeaf[meta.LeafField] {
			live = true
		}
		if !live {
			rk := rootedPathKey{RootPkg: configPkgRel, RootStruct: configStructName, Path: meta.RootedChain}
			if isRootedPathConsumed(rk, directConsumedRooted) {
				live = true
			}
		}
		if !live {
			for _, target := range leafCopyTargets[meta.LeafField] {
				if isRootedPathConsumed(target, directConsumedRooted) {
					live = true
					break
				}
			}
		}
		if !live {
			if extField, ok := idx.TomlDecoders[meta.TomlKey]; ok && directConsumedLeaf[extField] {
				live = true
			}
		}
		if !live {
			unread[meta.TomlKey] = true
		}
	}

	return metas, unread, nil
}

func isRootedPathConsumed(rk rootedPathKey, consumed map[rootedPathKey]bool) bool {
	if consumed[rk] {
		return true
	}
	// Also check if a prefix struct (e.g. `EnsembleConfig.Cache`) was consumed as a whole value
	// AND its leaf field was consumed on that sub-struct type.
	return false
}

func exprCallsRepoConsumer(idx *repoASTIndex, pf *parsedFunc, origins map[string]varOrigin, expr ast.Expr, configPkgRel string) bool {
	if expr == nil {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, callee := range resolveCalleeFuncs(idx, pf, origins, call.Fun) {
			if callee.Key.PkgRel != configPkgRel {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func scanBlockForFieldReads(
	idx *repoASTIndex,
	pf *parsedFunc,
	block *ast.BlockStmt,
	origins map[string]varOrigin,
	configPkgRel string,
	configStructName string,
	inConfigSelfMgmt bool,
	configOwnedStructs map[structTypeKey]int,
	directConsumedLeaf map[structFieldKey]bool,
	directConsumedRooted map[rootedPathKey]bool,
	leafCopyTargets map[structFieldKey][]rootedPathKey,
	addCopyEdge func(src, dst rootedPathKey),
) {
	if block == nil {
		return
	}

	// Collect AST nodes that are pure write LHS targets (`lhs = rhs`) or pure
	// copy-guard conditions (`if src.F > 0 { dst.G = src.F }`) so we distinguish
	// pure struct-to-struct adapter copies from genuine runtime reads.
	writeLHSSet := map[*ast.SelectorExpr]bool{}
	copyHandledSelectors := map[*ast.SelectorExpr]bool{}

	recordPureCopyStmt := func(assign *ast.AssignStmt, guardCond ast.Expr) bool {
		if assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return false
		}
		lhsSel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok {
			return false
		}
		dstRoot, dstPath, dstFieldType, dstOK := resolveSelectorRootedPath(idx, pf, origins, lhsSel)
		if !dstOK || configOwnedStructs[dstRoot] > 0 {
			return false
		}

		// Case 1: composite literal assignment `dst.Sub = SubStruct{ FieldA: src.X, ... }`
		if comp, isComp := assign.Rhs[0].(*ast.CompositeLit); isComp {
			anyField := false
			for _, elt := range comp.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				kIdent, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				sels := collectSelectorExprs(kv.Value)
				if len(sels) == 0 {
					continue
				}
				anyField = true
				targetRK := rootedPathKey{
					RootPkg:    dstRoot.PkgRel,
					RootStruct: dstRoot.Name,
					Path:       dstPath + "." + kIdent.Name,
				}
				for _, rSel := range sels {
					if sfk, srcRK, ok := resolveReadSelector(idx, pf, origins, rSel); ok {
						copyHandledSelectors[rSel] = true
						leafCopyTargets[sfk] = append(leafCopyTargets[sfk], targetRK)
						addCopyEdge(srcRK, targetRK)
					}
				}
			}
			return anyField
		}

		// Case 2: whole-struct copy `dst.Sub = src.Sub` where both have struct type `dstFieldType`
		if dstFieldType.Name != "" {
			if st := idx.Structs[idx.canonicalType(dstFieldType)]; st != nil {
				if srcVO, ok := resolveExprOrigin(idx, pf, origins, assign.Rhs[0]); ok && srcVO.RootType.Name != "" {
					if rSel, isSel := unwrapPureSelector(assign.Rhs[0]); isSel {
						copyHandledSelectors[rSel] = true
					}
					srcRoot := idx.canonicalType(srcVO.RootType)
					if guardCond != nil && srcVO.RootChain != "" {
						for _, gs := range collectSelectorExprs(guardCond) {
							if _, gRK, gOK := resolveReadSelector(idx, pf, origins, gs); gOK && strings.HasPrefix(gRK.Path, srcVO.RootChain) {
								copyHandledSelectors[gs] = true
							}
						}
					}
					targetBase := rootedPathKey{RootPkg: dstRoot.PkgRel, RootStruct: dstRoot.Name, Path: dstPath}
					if srcVO.RootChain != "" {
						addCopyEdge(rootedPathKey{RootPkg: srcRoot.PkgRel, RootStruct: srcRoot.Name, Path: srcVO.RootChain}, targetBase)
					}
					for childField := range st.Fields {
						addCopyEdge(
							rootedPathKey{RootPkg: srcRoot.PkgRel, RootStruct: srcRoot.Name, Path: joinRootedPath(srcVO.RootChain, childField)},
							rootedPathKey{RootPkg: dstRoot.PkgRel, RootStruct: dstRoot.Name, Path: dstPath + "." + childField},
						)
					}
					return true
				}
			}
		}

		// Do not suppress RHS selectors if the RHS invokes a non-config repo function
		// (e.g., `cfg.Summary = NewSummaryGenerator(SummaryGeneratorConfig{MaxTokens: ...})`),
		// since the callee genuinely consumes its struct/scalar arguments.
		if exprCallsRepoConsumer(idx, pf, origins, assign.Rhs[0], configPkgRel) {
			return false
		}

		// Case 3: scalar/converted field copy `dst.Sub.Field = conv(src.Sub.Field)`
		rhsSels := collectSelectorExprs(assign.Rhs[0])
		if len(rhsSels) == 0 {
			// Also handle `if src.Flag { dst.Flag = true }`!
			if litIdent, isIdent := assign.Rhs[0].(*ast.Ident); isIdent && (litIdent.Name == "true" || litIdent.Name == "false") && guardCond != nil {
				guardSels := collectSelectorExprs(guardCond)
				if len(guardSels) >= 1 {
					targetRK := rootedPathKey{RootPkg: dstRoot.PkgRel, RootStruct: dstRoot.Name, Path: dstPath}
					matched := false
					for _, gSel := range guardSels {
						if sfk, srcRK, ok := resolveReadSelector(idx, pf, origins, gSel); ok {
							copyHandledSelectors[gSel] = true
							leafCopyTargets[sfk] = append(leafCopyTargets[sfk], targetRK)
							addCopyEdge(srcRK, targetRK)
							matched = true
						}
					}
					return matched
				}
			}
			return false
		}
		targetRK := rootedPathKey{RootPkg: dstRoot.PkgRel, RootStruct: dstRoot.Name, Path: dstPath}
		matchedSrc := map[structFieldKey]bool{}
		for _, rSel := range rhsSels {
			if sfk, srcRK, ok := resolveReadSelector(idx, pf, origins, rSel); ok {
				copyHandledSelectors[rSel] = true
				matchedSrc[sfk] = true
				leafCopyTargets[sfk] = append(leafCopyTargets[sfk], targetRK)
				addCopyEdge(srcRK, targetRK)
			}
		}
		if len(matchedSrc) > 0 && guardCond != nil {
			for _, gSel := range collectSelectorExprs(guardCond) {
				if sfk, _, ok := resolveReadSelector(idx, pf, origins, gSel); ok && matchedSrc[sfk] {
					copyHandledSelectors[gSel] = true
				}
			}
		}
		return len(matchedSrc) > 0
	}

	if !inConfigSelfMgmt {
		ast.Inspect(block, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.AssignStmt:
				if s.Tok == token.ASSIGN {
					for _, lhs := range s.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok {
							writeLHSSet[sel] = true
						}
					}
				}
				recordPureCopyStmt(s, nil)
			case *ast.IfStmt:
				if s.Else == nil && s.Init == nil && s.Body != nil && len(s.Body.List) == 1 {
					if assign, ok := s.Body.List[0].(*ast.AssignStmt); ok {
						recordPureCopyStmt(assign, s.Cond)
					}
				}
			}
			return true
		})
	}

	// Inside internal/config self-mgmt (e.g. loadWithCWD), ONLY:
	// 1) arguments passed directly to external packages, or
	// 2) intra-Config alias folding (`if cfg.Alias { cfg.Canonical = true }`, where Alias != Canonical)
	// count across the config boundary.
	if inConfigSelfMgmt {
		cfgRoot := structTypeKey{PkgRel: configPkgRel, Name: configStructName}
		ast.Inspect(block, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.IfStmt:
				if s.Body != nil {
					var guardRKs []rootedPathKey
					for _, gSel := range collectSelectorExprs(s.Cond) {
						if _, gRK, ok := resolveReadSelector(idx, pf, origins, gSel); ok && gRK.RootPkg == cfgRoot.PkgRel && gRK.RootStruct == cfgRoot.Name {
							guardRKs = append(guardRKs, gRK)
						}
					}
					if len(guardRKs) > 0 {
						for _, stmt := range s.Body.List {
							assign, ok := stmt.(*ast.AssignStmt)
							if !ok || assign.Tok != token.ASSIGN {
								continue
							}
							for _, lhs := range assign.Lhs {
								lhsSel, ok := lhs.(*ast.SelectorExpr)
								if !ok {
									continue
								}
								dstRoot, dstPath, _, dstOK := resolveSelectorRootedPath(idx, pf, origins, lhsSel)
								if !dstOK || dstRoot != cfgRoot {
									continue
								}
								dstRK := rootedPathKey{RootPkg: dstRoot.PkgRel, RootStruct: dstRoot.Name, Path: dstPath}
								for _, srcRK := range guardRKs {
									if srcRK != dstRK {
										addCopyEdge(srcRK, dstRK)
									}
								}
							}
						}
					}
				}
			case *ast.CallExpr:
				sel, ok := s.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				targetPkg, isImport := pf.Imports[pkgIdent.Name]
				if !isImport || targetPkg == configPkgRel {
					return true
				}
				for _, arg := range s.Args {
					for _, argSel := range collectSelectorExprs(arg) {
						if sfk, srcRK, ok := resolveReadSelector(idx, pf, origins, argSel); ok {
							directConsumedLeaf[sfk] = true
							if srcRK.RootStruct != "" && srcRK.Path != "" {
								directConsumedRooted[srcRK] = true
							}
						}
					}
				}
			}
			return true
		})
		return
	}

	// Propagate whole-substruct arguments and struct composite literals, and mark
	// genuine runtime field reads.
	ast.Inspect(block, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.FuncLit:
			nested := map[string]varOrigin{}
			for k, v := range origins {
				nested[k] = v
			}
			if e.Type.Params != nil {
				for _, p := range e.Type.Params.List {
					pt, _ := resolveASTTypeExpr(p.Type, pf.Key.PkgRel, pf.Imports)
					if pt.Name != "" {
						for _, pn := range p.Names {
							nested[pn.Name] = varOrigin{Type: pt, RootType: pt}
						}
					}
				}
			}
			fakeFn := *pf
			fakeFn.Decl = &ast.FuncDecl{Name: pf.Decl.Name, Type: e.Type, Body: e.Body}
			nestedOrigins := inferFuncVarOrigins(idx, &fakeFn, nested)
			scanBlockForFieldReads(
				idx,
				pf,
				e.Body,
				nestedOrigins,
				configPkgRel,
				configStructName,
				inConfigSelfMgmt,
				configOwnedStructs,
				directConsumedLeaf,
				directConsumedRooted,
				leafCopyTargets,
				addCopyEdge,
			)
			return false
		case *ast.CompositeLit:
			compType, _ := resolveASTTypeExpr(e.Type, pf.Key.PkgRel, pf.Imports)
			compType = idx.canonicalType(compType)
			if compType.Name != "" && configOwnedStructs[compType] == 0 {
				if compSt := idx.Structs[compType]; compSt != nil {
					for _, elt := range e.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						kIdent, ok := kv.Key.(*ast.Ident)
						if !ok {
							continue
						}
						_, fi, hasField := idx.lookupField(compType, kIdent.Name)
						if !hasField {
							continue
						}
						fieldStructType := idx.canonicalType(structTypeKey{PkgRel: fi.TypePkg, Name: fi.TypeName})
						if fieldStructType.Name != "" && !fi.IsComposite {
							if subSt := idx.Structs[fieldStructType]; subSt != nil {
								if valVO, ok := resolveExprOrigin(idx, pf, origins, kv.Value); ok && valVO.RootType.Name != "" {
									if valSel, isSel := unwrapPureSelector(kv.Value); isSel {
										copyHandledSelectors[valSel] = true
									}
									valRoot := idx.canonicalType(valVO.RootType)
									for childField := range subSt.Fields {
										srcChildRK := rootedPathKey{
											RootPkg:    valRoot.PkgRel,
											RootStruct: valRoot.Name,
											Path:       joinRootedPath(valVO.RootChain, childField),
										}
										addCopyEdge(srcChildRK, rootedPathKey{
											RootPkg:    compType.PkgRel,
											RootStruct: compType.Name,
											Path:       kIdent.Name + "." + childField,
										})
										addCopyEdge(srcChildRK, rootedPathKey{
											RootPkg:    fieldStructType.PkgRel,
											RootStruct: fieldStructType.Name,
											Path:       childField,
										})
									}
								}
								continue
							}
						}
						if !exprCallsRepoConsumer(idx, pf, origins, kv.Value, configPkgRel) {
							targetRK := rootedPathKey{
								RootPkg:    compType.PkgRel,
								RootStruct: compType.Name,
								Path:       kIdent.Name,
							}
							for _, rSel := range collectSelectorExprs(kv.Value) {
								if sfk, srcRK, ok := resolveReadSelector(idx, pf, origins, rSel); ok {
									copyHandledSelectors[rSel] = true
									leafCopyTargets[sfk] = append(leafCopyTargets[sfk], targetRK)
									addCopyEdge(srcRK, targetRK)
								}
							}
						}
					}
				}
			}
		case *ast.CallExpr:
			// Check arguments that pass a struct value `x.Sub` or `localSub` into a callee parameter `paramType`.
			for _, callee := range resolveCalleeFuncs(idx, pf, origins, e.Fun) {
				if isCeremonialValidatorOrDryRunFunc(callee.Key, callee.FileRel) {
					continue
				}
				var flatParamTypes []structTypeKey
				for _, pg := range callee.ParamTypes {
					if len(pg.Names) == 0 {
						flatParamTypes = append(flatParamTypes, pg.Type)
					} else {
						for range pg.Names {
							flatParamTypes = append(flatParamTypes, pg.Type)
						}
					}
				}
				for argIdx, arg := range e.Args {
					if argIdx >= len(flatParamTypes) {
						break
					}
					pType := idx.canonicalType(flatParamTypes[argIdx])
					if st := idx.Structs[pType]; st != nil {
						if argVO, ok := resolveExprOrigin(idx, pf, origins, arg); ok && argVO.RootType.Name != "" {
							if argSel, isSel := unwrapPureSelector(arg); isSel {
								copyHandledSelectors[argSel] = true
							}
							argRoot := idx.canonicalType(argVO.RootType)
							for childField := range st.Fields {
								addCopyEdge(
									rootedPathKey{RootPkg: argRoot.PkgRel, RootStruct: argRoot.Name, Path: joinRootedPath(argVO.RootChain, childField)},
									rootedPathKey{RootPkg: pType.PkgRel, RootStruct: pType.Name, Path: childField},
								)
							}
						}
					}
				}
			}
		case *ast.SelectorExpr:
			if writeLHSSet[e] || copyHandledSelectors[e] {
				return true
			}
			// Ignore intermediate struct selectors in `a.B.C`? No — only leaves
			// match `metas` anyway, and marking `(Root, "B.C")` on leaf `C` is exact!
			if sfk, srcRK, ok := resolveReadSelector(idx, pf, origins, e); ok {
				// Ignore method selectors (e.g. `cfg.Redaction.ToRedactionLibConfig` where Sel is a method, not a struct field):
				// `resolveReadSelector` uses `idx.lookupField`, which ONLY matches struct fields, never methods!
				_, fi, _ := idx.lookupField(structTypeKey{PkgRel: sfk.PkgRel, Name: sfk.Struct}, sfk.Field)
				isSubStruct := fi.TypeName != "" && !fi.IsComposite && idx.Structs[idx.canonicalType(structTypeKey{PkgRel: fi.TypePkg, Name: fi.TypeName})] != nil
				if !isSubStruct {
					directConsumedLeaf[sfk] = true
					if srcRK.RootStruct != "" && srcRK.Path != "" {
						directConsumedRooted[srcRK] = true
					}
				}
			}
		}
		return true
	})
}

func resolveSelectorRootedPath(
	idx *repoASTIndex,
	pf *parsedFunc,
	origins map[string]varOrigin,
	sel *ast.SelectorExpr,
) (structTypeKey, string, structTypeKey, bool) {
	baseVO, ok := resolveExprOrigin(idx, pf, origins, sel.X)
	if !ok || baseVO.Type.Name == "" {
		return structTypeKey{}, "", structTypeKey{}, false
	}
	_, fi, found := idx.lookupField(baseVO.Type, sel.Sel.Name)
	if !found {
		return structTypeKey{}, "", structTypeKey{}, false
	}
	root := baseVO.RootType
	if root.Name == "" {
		root = baseVO.Type
	}
	path := sel.Sel.Name
	if baseVO.RootChain != "" {
		path = baseVO.RootChain + "." + sel.Sel.Name
	}
	return idx.canonicalType(root), path, structTypeKey{PkgRel: fi.TypePkg, Name: fi.TypeName}, true
}

func resolveReadSelector(
	idx *repoASTIndex,
	pf *parsedFunc,
	origins map[string]varOrigin,
	sel *ast.SelectorExpr,
) (structFieldKey, rootedPathKey, bool) {
	baseVO, ok := resolveExprOrigin(idx, pf, origins, sel.X)
	if !ok || baseVO.Type.Name == "" {
		return structFieldKey{}, rootedPathKey{}, false
	}
	sfk, _, found := idx.lookupField(baseVO.Type, sel.Sel.Name)
	if !found {
		return structFieldKey{}, rootedPathKey{}, false
	}
	root := baseVO.RootType
	if root.Name == "" {
		root = baseVO.Type
	}
	root = idx.canonicalType(root)
	path := sel.Sel.Name
	if baseVO.RootChain != "" {
		path = baseVO.RootChain + "." + sel.Sel.Name
	}
	return sfk, rootedPathKey{RootPkg: root.PkgRel, RootStruct: root.Name, Path: path}, true
}

func unwrapPureSelector(expr ast.Expr) (*ast.SelectorExpr, bool) {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return unwrapPureSelector(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return unwrapPureSelector(e.X)
		}
	case *ast.StarExpr:
		return unwrapPureSelector(e.X)
	case *ast.SelectorExpr:
		return e, true
	}
	return nil, false
}

func collectSelectorExprs(expr ast.Expr) []*ast.SelectorExpr {
	var out []*ast.SelectorExpr
	if expr == nil {
		return nil
	}
	ast.Inspect(expr, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			out = append(out, sel)
		}
		return true
	})
	return out
}
