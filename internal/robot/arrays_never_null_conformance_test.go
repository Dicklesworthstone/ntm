package robot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// arraysNeverNullExceptions lists the ONLY fields allowed to marshal as JSON
// null where the schema says array. Every entry must carry a justification.
// Key format: "<GoTypeName>.<json_path>". Keep this list empty unless a field
// deliberately distinguishes "not computed" (null) from "computed, empty" ([])
// AND documents that distinction on the field.
var arraysNeverNullExceptions = map[string]string{
	// (none) — the terminal encoder normalizes nil slices via
	// EnsureArraysNeverNull, so no registered envelope may emit a null array.
}

// TestArraysNeverNullRegistryWalk is the D2 conformance test
// (bd-ws3-contract-breadth-psvyu.2): it walks EVERY registered robot schema
// type, instantiates it zero-valued, runs it through the same normalization
// the terminal encoder applies (EnsureArraysNeverNull), marshals it, and
// asserts that no field whose schema type is "array" encodes as JSON null.
//
// Coverage is by construction: any new envelope registered in SchemaCommand
// (or via MustRegisterSchemaCommand) is picked up automatically.
func TestArraysNeverNullRegistryWalk(t *testing.T) {
	registry := GetRobotRegistry()
	if len(registry.SchemaTypes) < 80 {
		t.Fatalf("registry breadth sanity: expected >=80 registered schema types, got %d", len(registry.SchemaTypes))
	}

	usedExceptions := make(map[string]bool)
	for _, name := range registry.SchemaTypes {
		binding, ok := registry.SchemaBinding(name)
		if !ok {
			t.Fatalf("schema type %q has no binding", name)
		}
		typ := reflect.TypeOf(binding)
		for typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		t.Run(name, func(t *testing.T) {
			zero := reflect.New(typ)
			EnsureArraysNeverNull(zero.Interface())
			data, err := json.Marshal(zero.Interface())
			if err != nil {
				t.Fatalf("marshal zero %s: %v", typ.Name(), err)
			}
			var doc interface{}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("unmarshal zero %s: %v", typ.Name(), err)
			}
			var violations []string
			checkNoNullArrays(typ, doc, typ.Name(), &violations, usedExceptions, make(map[reflect.Type]bool))
			for _, v := range violations {
				t.Errorf("null array (contract: arrays are never null): %s", v)
			}
		})
	}

	for key := range arraysNeverNullExceptions {
		if !usedExceptions[key] {
			t.Errorf("stale arrays-never-null exception %q: no matching field emitted null", key)
		}
	}
}

// checkNoNullArrays aligns the static type with the marshaled JSON document
// and records every position where the schema says array but the JSON is null.
func checkNoNullArrays(t reflect.Type, doc interface{}, path string, violations *[]string, used map[string]bool, inProgress map[reflect.Type]bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// Custom marshalers own their representation; the static field layout no
	// longer describes the JSON.
	if t.Implements(jsonMarshalerType) || reflect.PtrTo(t).Implements(jsonMarshalerType) {
		return
	}

	switch t.Kind() {
	case reflect.Struct:
		if inProgress[t] {
			return // recursive type; zero value terminates anyway
		}
		inProgress[t] = true
		defer delete(inProgress, t)

		m, ok := doc.(map[string]interface{})
		if !ok {
			return
		}
		walkStructFields(t, m, path, violations, used, inProgress)

	case reflect.Slice:
		if isByteSlice(t) {
			return
		}
		if doc == nil {
			recordNullArray(path, violations, used)
			return
		}
		arr, ok := doc.([]interface{})
		if !ok {
			return
		}
		for i, elem := range arr {
			checkNoNullArrays(t.Elem(), elem, fmt.Sprintf("%s[%d]", path, i), violations, used, inProgress)
		}

	case reflect.Map:
		m, ok := doc.(map[string]interface{})
		if !ok {
			return
		}
		for key, val := range m {
			checkNoNullArrays(t.Elem(), val, path+"."+key, violations, used, inProgress)
		}
	}
}

func walkStructFields(t reflect.Type, m map[string]interface{}, path string, violations *[]string, used map[string]bool, inProgress map[reflect.Type]bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}
		if field.Anonymous {
			ft := field.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && jsonTag == "" {
				// Embedded struct: fields are flattened into this object.
				walkStructFields(ft, m, path, violations, used, inProgress)
				continue
			}
		}
		fieldName, _ := parseJSONTag(jsonTag)
		if fieldName == "" {
			fieldName = field.Name
		}
		val, present := m[fieldName]
		if !present {
			continue // omitted (omitempty) — omitted is not null
		}
		fieldPath := path + "." + fieldName

		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice && !isByteSlice(ft) &&
			!ft.Implements(jsonMarshalerType) && !reflect.PtrTo(ft).Implements(jsonMarshalerType) {
			if val == nil {
				recordFieldNullArray(t.Name(), fieldName, fieldPath, violations, used)
				continue
			}
		}
		checkNoNullArrays(field.Type, val, fieldPath, violations, used, inProgress)
	}
}

func recordFieldNullArray(typeName, fieldName, path string, violations *[]string, used map[string]bool) {
	key := typeName + "." + fieldName
	if _, ok := arraysNeverNullExceptions[key]; ok {
		used[key] = true
		return
	}
	*violations = append(*violations, path)
}

func recordNullArray(path string, violations *[]string, used map[string]bool) {
	// Non-field positions (slice/map elements) have no exception mechanism:
	// nested nulls where an array belongs are always violations.
	*violations = append(*violations, path)
}

// TestEnsureArraysNeverNullNormalizesNestedShapes proves the normalizer used
// by the terminal encoder repairs nil slices behind pointers, maps, nested
// structs, and slice elements — the shapes real envelopes use.
// TestNormalizeArraysNeverNullValuePayloads proves the VALUE (unaddressable)
// encode path honors the contract too: EnsureArraysNeverNull silently no-ops
// on non-pointer payloads, so the terminal encoders route through
// NormalizeArraysNeverNull, which must return a normalized copy. Every
// registered envelope is exercised by value here.
func TestNormalizeArraysNeverNullValuePayloads(t *testing.T) {
	registry := GetRobotRegistry()
	usedExceptions := make(map[string]bool)
	for _, name := range registry.SchemaTypes {
		binding, ok := registry.SchemaBinding(name)
		if !ok {
			t.Fatalf("schema type %q has no binding", name)
		}
		typ := reflect.TypeOf(binding)
		for typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		t.Run(name, func(t *testing.T) {
			zero := reflect.New(typ).Elem().Interface() // pass by value, not pointer
			normalized := NormalizeArraysNeverNull(zero)
			data, err := json.Marshal(normalized)
			if err != nil {
				t.Fatalf("marshal normalized value %s: %v", typ.Name(), err)
			}
			var doc interface{}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("unmarshal normalized value %s: %v", typ.Name(), err)
			}
			var violations []string
			checkNoNullArrays(typ, doc, typ.Name(), &violations, usedExceptions, make(map[reflect.Type]bool))
			for _, v := range violations {
				t.Errorf("null array via value payload (contract: arrays are never null): %s", v)
			}
		})
	}
}

func TestEnsureArraysNeverNullNormalizesNestedShapes(t *testing.T) {
	type inner struct {
		Items []string `json:"items"`
	}
	type outer struct {
		Direct   []int            `json:"direct"`
		Ptr      *inner           `json:"ptr"`
		Nested   inner            `json:"nested"`
		ByMap    map[string]inner `json:"by_map"`
		Elems    []inner          `json:"elems"`
		Optional []string         `json:"optional,omitempty"`
		Raw      json.RawMessage  `json:"raw,omitempty"`
		Blob     []byte           `json:"blob,omitempty"`
		IfaceVal interface{}      `json:"iface_val"`
		StrMap   map[string][]int `json:"str_map"`
	}

	out := &outer{
		Ptr:    &inner{},
		ByMap:  map[string]inner{"a": {}},
		Elems:  []inner{{}},
		StrMap: map[string][]int{"k": nil},
	}
	EnsureArraysNeverNull(out)
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "null") && !strings.Contains(s, `"iface_val":null`) {
		t.Fatalf("normalized output still contains null arrays: %s", s)
	}
	for _, want := range []string{`"direct":[]`, `"items":[]`, `"str_map":{"k":[]}`} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %s in output, got: %s", want, s)
		}
	}
}
