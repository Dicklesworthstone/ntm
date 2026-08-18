// Package robot provides machine-readable output for AI agents.
// arrays_never_null.go enforces the documented "arrays are never null"
// contract (bd-ws3-contract-breadth-psvyu.2): every slice-typed field in a
// robot envelope must marshal as [] (or be omitted via omitempty), never as
// JSON null. Instead of hand-initializing every constructor, the terminal
// encode paths normalize envelopes through EnsureArraysNeverNull, and a
// registry-walk conformance test (arrays_never_null_conformance_test.go)
// proves the normalizer covers every registered envelope type.
package robot

import (
	"encoding/json"
	"reflect"
)

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// EnsureArraysNeverNull walks v (which should be a pointer to a robot
// envelope) and replaces every reachable nil slice with an empty, non-nil
// slice so it marshals as [] instead of null.
//
// Rules:
//   - []byte (and other byte slices) are skipped: they marshal as base64
//     strings, not JSON arrays, and types like json.RawMessage break when
//     forced to a non-nil empty value.
//   - Values whose types implement json.Marshaler are left alone; their
//     marshaler owns the representation.
//   - Unexported fields are skipped (encoding/json ignores them anyway).
//   - Map values and interface contents are normalized via copy-and-set
//     because they are not addressable in place.
func EnsureArraysNeverNull(v interface{}) {
	if v == nil {
		return
	}
	visited := make(map[uintptr]bool)
	ensureValue(reflect.ValueOf(v), visited)
}

// NormalizeArraysNeverNull applies the arrays-never-null contract and returns
// the value to encode. Pointer payloads are normalized in place (and returned
// unchanged); value payloads — which EnsureArraysNeverNull cannot mutate
// because they are unaddressable — are copied into an addressable temporary,
// normalized there, and the normalized copy is returned. Terminal encode
// paths must encode the RETURN value, not the argument.
func NormalizeArraysNeverNull(v interface{}) interface{} {
	if v == nil {
		return v
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Map {
		EnsureArraysNeverNull(v)
		return v
	}
	copied := reflect.New(rv.Type())
	copied.Elem().Set(rv)
	if elem := copied.Elem(); elem.Kind() == reflect.Slice && elem.IsNil() && !isByteSlice(elem.Type()) {
		elem.Set(reflect.MakeSlice(elem.Type(), 0, 0))
	}
	EnsureArraysNeverNull(copied.Interface())
	return copied.Elem().Interface()
}

func ensureValue(v reflect.Value, visited map[uintptr]bool) {
	if !v.IsValid() {
		return
	}
	t := v.Type()
	// Custom marshalers own their JSON representation.
	if t.Implements(jsonMarshalerType) && t.Kind() != reflect.Interface {
		return
	}

	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		ptr := v.Pointer()
		if visited[ptr] {
			return
		}
		visited[ptr] = true
		ensureValue(v.Elem(), visited)

	case reflect.Interface:
		if v.IsNil() {
			return
		}
		elem := v.Elem()
		if elem.Kind() == reflect.Ptr || elem.Kind() == reflect.Map {
			// Pointer/map contents are shared; normalize through them.
			ensureValue(elem, visited)
			return
		}
		if !v.CanSet() {
			return
		}
		// Value payloads (structs, slices) must be copied, normalized,
		// and stored back.
		copied := reflect.New(elem.Type()).Elem()
		copied.Set(elem)
		ensureValue(copied, visited)
		if copied.Kind() == reflect.Slice && copied.IsNil() && !isByteSlice(copied.Type()) {
			copied.Set(reflect.MakeSlice(copied.Type(), 0, 0))
		}
		v.Set(copied)

	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" { // unexported
				continue
			}
			if field.Tag.Get("json") == "-" && !field.Anonymous {
				continue
			}
			fv := v.Field(i)
			if fv.Kind() == reflect.Slice && fv.CanSet() && fv.IsNil() && !isByteSlice(fv.Type()) && !fv.Type().Implements(jsonMarshalerType) {
				fv.Set(reflect.MakeSlice(fv.Type(), 0, 0))
				continue
			}
			ensureValue(fv, visited)
		}

	case reflect.Slice:
		if v.IsNil() || isByteSlice(t) {
			return
		}
		for i := 0; i < v.Len(); i++ {
			elem := v.Index(i)
			if elem.Kind() == reflect.Slice && elem.CanSet() && elem.IsNil() && !isByteSlice(elem.Type()) {
				elem.Set(reflect.MakeSlice(elem.Type(), 0, 0))
				continue
			}
			ensureValue(elem, visited)
		}

	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			ensureValue(v.Index(i), visited)
		}

	case reflect.Map:
		if v.IsNil() {
			return
		}
		iter := v.MapRange()
		for iter.Next() {
			mv := iter.Value()
			switch mv.Kind() {
			case reflect.Ptr, reflect.Map:
				ensureValue(mv, visited)
			case reflect.Struct, reflect.Slice, reflect.Interface, reflect.Array:
				copied := reflect.New(mv.Type()).Elem()
				copied.Set(mv)
				if copied.Kind() == reflect.Slice && copied.IsNil() && !isByteSlice(copied.Type()) {
					copied.Set(reflect.MakeSlice(copied.Type(), 0, 0))
				} else {
					ensureValue(copied, visited)
				}
				v.SetMapIndex(iter.Key(), copied)
			}
		}
	}
}

func isByteSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}
