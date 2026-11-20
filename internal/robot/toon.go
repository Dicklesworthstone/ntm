// Package robot provides machine-readable output for AI agents.
// toon.go implements TOON (Token-Oriented Object Notation) encoding by
// delegating to the toon_rust encoder binary (`tru`).
//
// TOON is a token-efficient serialization format designed for LLM consumption.
// This implementation supports:
//   - Uniform arrays of objects (tabular format)
//   - Primitive values (strings, numbers, booleans, null)
//   - Simple objects with scalar fields
//
// For unsupported shapes (deeply nested structures), the encoder returns an error.
// Use FormatAuto to fall back to JSON for such payloads.
//
// Reference: https://github.com/toon-format/spec
package robot

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// toonEncode encodes a payload as TOON format.
// Returns an error for unsupported payload shapes.
func toonEncode(payload any, delimiter string) (string, error) {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("json marshal: %w", err)
	}

	truPath, err := toonBinaryPath()
	if err != nil {
		// Fallback to pure Go implementation if rust binary is missing
		return toonEncodePureGo(payload, delimiter)
	}

	args := []string{"--encode"}
	if delimArg := toonDelimiterArg(delimiter); delimArg != "" {
		args = append(args, "--delimiter", delimArg)
	}

	cmd := exec.Command(truPath, args...)
	cmd.Stdin = bytes.NewReader(jsonBytes)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("toon_rust encode failed: %s", errMsg)
	}

	return stdout.String(), nil
}

// toonEncodePureGo encodes a payload as TOON using the pure Go implementation.
func toonEncodePureGo(payload any, delimiter string) (string, error) {
	enc := &toonEncoder{delimiter: toonDelimiterChar(delimiter)}
	v := reflect.ValueOf(payload)

	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "null\n", nil
		}
		v = v.Elem()
	}

	if !v.IsValid() {
		return "null\n", nil
	}

	if scalar, ok, err := enc.encodeMarshalerValue(v); err != nil {
		return "", err
	} else if ok {
		return scalar + "\n", nil
	}

	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		return enc.renderArray(v)
	case reflect.Map, reflect.Struct:
		return enc.renderObject(v, 0)
	default:
		encoded, err := enc.encodeValue(v)
		if err != nil {
			return "", err
		}
		return encoded + "\n", nil
	}
}

var (
	toonBinaryOnce       sync.Once
	toonBinaryPathCached string
	toonBinaryErr        error
)

func toonBinaryPath() (string, error) {
	if envPath, err := toonBinaryFromEnv(); err != nil {
		return "", err
	} else if envPath != "" {
		return envPath, nil
	}

	toonBinaryOnce.Do(func() {
		for _, candidate := range []string{"tru"} {
			path, err := exec.LookPath(candidate)
			if err != nil {
				continue
			}
			if !isToonRustBinary(path) {
				continue
			}
			toonBinaryPathCached = path
			return
		}

		toonBinaryErr = fmt.Errorf(
			"toon_rust encoder not found in PATH; install tru or set TOON_BIN/TOON_TRU_BIN to the toon_rust binary path",
		)
	})

	if toonBinaryErr != nil {
		return "", toonBinaryErr
	}
	return toonBinaryPathCached, nil
}

func toonBinaryFromEnv() (string, error) {
	for _, env := range []string{"TOON_TRU_BIN", "TOON_BIN"} {
		if val := strings.TrimSpace(os.Getenv(env)); val != "" {
			if !isToonRustBinary(val) {
				return "", fmt.Errorf("%s=%q does not appear to be toon_rust (expected tru)", env, val)
			}
			return val, nil
		}
	}
	return "", nil
}

func isToonRustBinary(path string) bool {
	// Distinguish toon_rust from:
	// - system `tr` (coreutils)
	// - the Node.js `toon` CLI (toon-format), which is not allowed here
	base := strings.ToLower(filepathBase(path))
	if base == "toon" || base == "toon.exe" {
		// Never accept (or invoke) the Node.js encoder as the TOON backend.
		return false
	}
	helpOut, _ := exec.Command(path, "--help").CombinedOutput()
	helpLower := strings.ToLower(string(helpOut))
	if strings.Contains(helpLower, "reference implementation in rust") {
		return true
	}

	verOut, _ := exec.Command(path, "--version").CombinedOutput()
	verLower := strings.ToLower(strings.TrimSpace(string(verOut)))
	return looksLikeToonRustVersion(verLower) || strings.HasPrefix(verLower, "toon_rust ")
}

func filepathBase(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func looksLikeToonRustVersion(verLower string) bool {
	// toon_rust prints: "tru X.Y.Z"
	// coreutils prints: "tr (GNU coreutils) X.Y"
	return strings.HasPrefix(verLower, "tru ") && len(verLower) >= 5 && verLower[4] >= '0' && verLower[4] <= '9'
}

func toonDelimiterArg(delimiter string) string {
	switch delimiter {
	case "", ",":
		return ","
	case "\t":
		return "tab"
	case "|":
		return "|"
	case "tab", "comma", "pipe":
		return delimiter
	default:
		return strings.TrimSpace(delimiter)
	}
}

// toonDelimiterChar resolves delimiter names ("tab", "comma", "pipe") and
// literal separator characters ("\t", ",", "|") to the actual 1-character
// delimiter emitted inside TOON rows and inline arrays.
func toonDelimiterChar(delimiter string) string {
	if delimiter == "\t" {
		return "\t"
	}
	switch strings.ToLower(strings.TrimSpace(delimiter)) {
	case "", ",", "comma":
		return ","
	case "tab":
		return "\t"
	case "|", "pipe":
		return "|"
	default:
		return strings.TrimSpace(delimiter)
	}
}

// toonHeaderDelim returns the TOON spec delimiter suffix placed inside array
// length brackets ("[N\t]", "[N|]", or "[N]" for the default comma delimiter).
func toonHeaderDelim(delim string) string {
	if delim == "\t" || delim == "|" {
		return delim
	}
	return ""
}

// toonEncoder holds encoding state.
type toonEncoder struct {
	delimiter string
}

// renderArray renders a slice/array as TOON tabular format.
func (enc *toonEncoder) renderArray(v reflect.Value) (string, error) {
	length := v.Len()
	if length == 0 {
		return "[]\n", nil
	}

	// Check if it's an array of objects (maps or structs)
	first := v.Index(0)
	for first.Kind() == reflect.Ptr || first.Kind() == reflect.Interface {
		if first.IsNil() {
			return "", fmt.Errorf("TOON: nil element in array")
		}
		first = first.Elem()
	}

	if first.Kind() == reflect.Map || first.Kind() == reflect.Struct {
		return enc.renderTabular(v)
	}

	// Primitive array: inline format
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("[%d%s]:", length, toonHeaderDelim(enc.delimiter)))
	for i := 0; i < length; i++ {
		elem := v.Index(i)
		encoded, err := enc.encodeValue(elem)
		if err != nil {
			return "", err
		}
		if i > 0 {
			buf.WriteString(enc.delimiter)
		}
		buf.WriteString(encoded)
	}
	buf.WriteString("\n")
	return buf.String(), nil
}

// renderTabular renders a uniform array of objects in TOON tabular format.
func (enc *toonEncoder) renderTabular(v reflect.Value) (string, error) {
	length := v.Len()
	if length == 0 {
		return "[]\n", nil
	}

	// Extract field names from the first element
	fields, err := enc.extractFields(v.Index(0))
	if err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("TOON: empty object in array")
	}

	// Check if tab delimiter is safe (no tabs/newlines in values)
	safeDelim := enc.delimiter
	if enc.delimiter == "\t" && !enc.isTabSafe(v, fields) {
		safeDelim = ","
	}

	var buf strings.Builder

	// Header: [count<delim>]{field1<delim>field2,...}:
	buf.WriteString(fmt.Sprintf("[%d%s]{%s}:\n", length, toonHeaderDelim(safeDelim), strings.Join(fields, safeDelim)))

	// Rows
	for i := 0; i < length; i++ {
		elem := v.Index(i)
		for elem.Kind() == reflect.Ptr || elem.Kind() == reflect.Interface {
			if elem.IsNil() {
				return "", fmt.Errorf("TOON: nil element at index %d", i)
			}
			elem = elem.Elem()
		}

		buf.WriteString(" ") // TOON requires single space indent for rows
		for j, field := range fields {
			val, err := enc.getFieldValue(elem, field)
			if err != nil {
				return "", err
			}
			encoded, err := enc.encodeValue(val)
			if err != nil {
				return "", fmt.Errorf("TOON: field %q at index %d: %w", field, i, err)
			}
			if j > 0 {
				buf.WriteString(safeDelim)
			}
			buf.WriteString(encoded)
		}
		buf.WriteString("\n")
	}

	return buf.String(), nil
}

// renderObject renders a map or struct as TOON key-value pairs.
func (enc *toonEncoder) renderObject(v reflect.Value, indent int) (string, error) {
	fields, err := enc.extractFields(v)
	if err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return "{}\n", nil
	}

	var buf strings.Builder
	indentStr := strings.Repeat("  ", indent)

	for _, field := range fields {
		val, err := enc.getFieldValue(v, field)
		if err != nil {
			return "", err
		}

		// Handle nil/invalid values
		if !val.IsValid() {
			buf.WriteString(indentStr + field + ": null\n")
			continue
		}

		for val.Kind() == reflect.Ptr || val.Kind() == reflect.Interface {
			if val.IsNil() {
				buf.WriteString(indentStr + field + ": null\n")
				val = reflect.Value{} // mark as handled
				break
			}
			val = val.Elem()
		}

		if !val.IsValid() {
			continue // already handled as null
		}

		// Check if value is complex (needs nested rendering)
		if scalar, ok, err := enc.encodeMarshalerValue(val); err != nil {
			return "", err
		} else if ok {
			buf.WriteString(indentStr + field + ": " + scalar + "\n")
		} else if val.Kind() == reflect.Slice || val.Kind() == reflect.Array {
			nested, err := enc.renderArray(val)
			if err != nil {
				return "", err
			}
			buf.WriteString(indentStr + field + nested)
		} else if val.Kind() == reflect.Map || val.Kind() == reflect.Struct {
			buf.WriteString(indentStr + field + ":\n")
			nested, err := enc.renderObject(val, indent+1)
			if err != nil {
				return "", err
			}
			buf.WriteString(nested)
		} else {
			encoded, err := enc.encodeValue(val)
			if err != nil {
				return "", err
			}
			buf.WriteString(indentStr + field + ": " + encoded + "\n")
		}
	}

	return buf.String(), nil
}

type toonStructField struct {
	name string
	val  reflect.Value
}

func parseToonJSONTag(tag string) (name string, omitEmpty bool, skip bool) {
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitEmpty = true
		}
	}
	return name, omitEmpty, false
}

func isEmptyToonValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	case reflect.Struct:
		return v.IsZero()
	}
	return false
}

// flattenStructFields promotes anonymous embedded structs (such as
// RobotResponse) and honors json:"-" and json:",omitempty" identically to
// encoding/json so pure-Go TOON matches tru's JSON-derived output.
func flattenStructFields(v reflect.Value) []toonStructField {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}

	t := v.Type()
	// First record explicit direct field names so outer fields shadow embedded ones.
	directNames := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tagName, _, skip := parseToonJSONTag(f.Tag.Get("json"))
		if skip {
			continue
		}
		if f.Anonymous && tagName == "" {
			ft := f.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				continue
			}
		}
		name := tagName
		if name == "" {
			name = f.Name
		}
		directNames[name] = true
	}

	out := make([]toonStructField, 0, t.NumField())
	seen := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tagName, omitEmpty, skip := parseToonJSONTag(f.Tag.Get("json"))
		if skip {
			continue
		}
		fv := v.Field(i)

		// Promote anonymous embedded struct fields when no explicit JSON name overrides them.
		if f.Anonymous && tagName == "" {
			embedded := fv
			if embedded.Kind() == reflect.Ptr {
				if embedded.IsNil() {
					continue
				}
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				for _, sub := range flattenStructFields(embedded) {
					if directNames[sub.name] || seen[sub.name] {
						continue
					}
					seen[sub.name] = true
					out = append(out, sub)
				}
				continue
			}
		}

		if omitEmpty && isEmptyToonValue(fv) {
			continue
		}
		name := tagName
		if name == "" {
			name = f.Name
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, toonStructField{name: name, val: fv})
	}
	return out
}

// encodeMarshalerValue encodes scalar-like json.Marshaler / encoding.TextMarshaler
// structs (such as time.Time) via their JSON scalar representation so they do
// not collapse into empty "{}" objects.
func (enc *toonEncoder) encodeMarshalerValue(v reflect.Value) (string, bool, error) {
	if !v.IsValid() || !v.CanInterface() {
		return "", false, nil
	}
	if v.Kind() != reflect.Struct {
		return "", false, nil
	}
	val := v.Interface()
	_, isJSON := val.(json.Marshaler)
	_, isText := val.(encoding.TextMarshaler)
	if !isJSON && !isText {
		return "", false, nil
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return "", false, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", false, err
	}
	switch d := decoded.(type) {
	case nil:
		return "null", true, nil
	case string:
		return enc.encodeString(d), true, nil
	case bool:
		if d {
			return "true", true, nil
		}
		return "false", true, nil
	case float64:
		return enc.formatFloat(d), true, nil
	default:
		// Object or array marshaler: let normal object/array rendering handle it.
		return "", false, nil
	}
}

// extractFields returns the field names from a map or struct.
func (enc *toonEncoder) extractFields(v reflect.Value) ([]string, error) {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Map:
		keys := v.MapKeys()
		fields := make([]string, len(keys))
		for i, key := range keys {
			if key.Kind() != reflect.String {
				return nil, fmt.Errorf("TOON: non-string map key")
			}
			fields[i] = key.String()
		}
		sort.Strings(fields)
		return fields, nil
	case reflect.Struct:
		entries := flattenStructFields(v)
		fields := make([]string, len(entries))
		for i, e := range entries {
			fields[i] = e.name
		}
		return fields, nil
	default:
		return nil, fmt.Errorf("TOON: expected map or struct, got %s", v.Kind())
	}
}

// getFieldValue retrieves a field value from a map or struct.
func (enc *toonEncoder) getFieldValue(v reflect.Value, field string) (reflect.Value, error) {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return reflect.Value{}, nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Map:
		// The map key type may be a named string type (e.g. robot.EventCategory,
		// `type EventCategory string`), not plain `string`. reflect.Value.MapIndex
		// panics if the key value's type isn't assignable to the map's key type,
		// so convert the string field name into the map's actual key type first.
		keyType := v.Type().Key()
		keyVal := reflect.ValueOf(field)
		if !keyVal.Type().AssignableTo(keyType) {
			if keyType.Kind() == reflect.String && keyVal.Type().ConvertibleTo(keyType) {
				keyVal = keyVal.Convert(keyType)
			} else {
				// Non-string-keyed map: there is no string field to look up here.
				// Return a zero Value (treated as absent) rather than panicking.
				return reflect.Value{}, nil
			}
		}
		return v.MapIndex(keyVal), nil
	case reflect.Struct:
		for _, e := range flattenStructFields(v) {
			if e.name == field {
				return e.val, nil
			}
		}
		return reflect.Value{}, nil
	default:
		return reflect.Value{}, fmt.Errorf("expected map or struct")
	}
}

// encodeValue encodes a single value as TOON.
func (enc *toonEncoder) encodeValue(v reflect.Value) (string, error) {
	if !v.IsValid() {
		return "null", nil
	}

	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "null", nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.String:
		return enc.encodeString(v.String()), nil
	case reflect.Bool:
		if v.Bool() {
			return "true", nil
		}
		return "false", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return enc.formatFloat(v.Float()), nil
	case reflect.Map, reflect.Struct, reflect.Slice, reflect.Array:
		if scalar, ok, err := enc.encodeMarshalerValue(v); err != nil {
			return "", err
		} else if ok {
			return scalar, nil
		}
		// Nested complex types in tabular rows: fall back to JSON inline
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(v.Interface()); err != nil {
			return "", fmt.Errorf("encoding nested value: %w", err)
		}
		// Remove trailing newline from JSON
		s := strings.TrimSuffix(buf.String(), "\n")
		return enc.encodeString(s), nil
	default:
		return "", fmt.Errorf("unsupported value type %s", v.Kind())
	}
}

// encodeString encodes a string, quoting only when necessary.
// Unquoted strings match: ^[A-Za-z_][A-Za-z0-9_]*$
func (enc *toonEncoder) encodeString(s string) string {
	if s == "" {
		return `""`
	}

	// Check if it needs quoting
	needsQuote := false
	if !toonIsIdentifierStart(rune(s[0])) {
		needsQuote = true
	} else {
		for _, c := range s[1:] {
			if !toonIsIdentifierChar(c) {
				needsQuote = true
				break
			}
		}
	}

	// Also quote if it looks like a TOON keyword or number
	if !needsQuote {
		switch s {
		case "true", "false", "null":
			needsQuote = true
		}
	}

	if !needsQuote {
		return s
	}

	// Quote and escape
	var buf strings.Builder
	buf.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\':
			buf.WriteString(`\\`)
		case '"':
			buf.WriteString(`\"`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			buf.WriteRune(c)
		}
	}
	buf.WriteByte('"')
	return buf.String()
}

// formatFloat formats a float without scientific notation.
func (enc *toonEncoder) formatFloat(f float64) string {
	// Use 'f' format to avoid scientific notation
	s := strconv.FormatFloat(f, 'f', -1, 64)
	// Remove trailing zeros after decimal point
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

// isTabSafe checks if tab delimiter is safe for all values.
func (enc *toonEncoder) isTabSafe(v reflect.Value, fields []string) bool {
	for i := 0; i < v.Len(); i++ {
		elem := v.Index(i)
		for elem.Kind() == reflect.Ptr || elem.Kind() == reflect.Interface {
			if elem.IsNil() {
				elem = reflect.Value{}
				break
			}
			elem = elem.Elem()
		}
		if !elem.IsValid() {
			continue
		}
		for _, field := range fields {
			val, err := enc.getFieldValue(elem, field)
			if err != nil {
				continue
			}
			if val.Kind() == reflect.String {
				s := val.String()
				if strings.ContainsAny(s, "\t\n\r") {
					return false
				}
			}
		}
	}
	return true
}

func toonIsIdentifierStart(c rune) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

func toonIsIdentifierChar(c rune) bool {
	return toonIsIdentifierStart(c) || (c >= '0' && c <= '9')
}
