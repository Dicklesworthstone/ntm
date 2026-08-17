package robot

import (
	"reflect"
	"testing"
)

// TestSchemaPaginationRegistryExhaustive is the D1 structural proof
// (bd-ws3-contract-breadth-psvyu.1): every list-shaped registered schema type
// must be explicitly flagged Paginated true/false in the single-declaration
// SchemaPagination map, every Paginated=true type must implement the
// PaginationInfo contract field, and the registry's Boundedness metadata may
// not contradict the flags. An UNFLAGGED list type FAILS this test, so new
// envelope types are covered at registration time by construction.
func TestSchemaPaginationRegistryExhaustive(t *testing.T) {
	registry := GetRobotRegistry()
	if len(registry.SchemaTypes) < 80 {
		t.Fatalf("registry breadth sanity: expected >=80 schema types, got %d", len(registry.SchemaTypes))
	}
	for _, v := range SchemaPaginationViolations() {
		t.Errorf("pagination flag violation: %s", v)
	}

	// The known-paginated core surfaces must stay flagged true.
	for _, name := range []string{"status", "snapshot", "history", "ensemble_modes"} {
		flag, ok := SchemaPagination[name]
		if !ok || !flag.Paginated {
			t.Errorf("schema type %q must be flagged Paginated=true", name)
		}
	}
}

// TestSchemaPaginationSurfaceFlagsExposed proves the flags are visible to
// machine consumers through the registry surface descriptors (capabilities),
// making the docs claim's scope machine-checkable.
func TestSchemaPaginationSurfaceFlagsExposed(t *testing.T) {
	registry := GetRobotRegistry()
	checked := 0
	for _, surface := range registry.Surfaces {
		if surface.SchemaType == "" {
			continue
		}
		flag, ok := SchemaPagination[surface.SchemaType]
		if !ok {
			if surface.Paginated != nil {
				t.Errorf("surface %q exposes paginated=%v but %q has no SchemaPagination flag", surface.Name, *surface.Paginated, surface.SchemaType)
			}
			continue
		}
		if surface.Paginated == nil {
			t.Errorf("surface %q must expose the %q pagination flag in the registry descriptor", surface.Name, surface.SchemaType)
			continue
		}
		if *surface.Paginated != flag.Paginated {
			t.Errorf("surface %q paginated=%v disagrees with SchemaPagination[%q]=%v", surface.Name, *surface.Paginated, surface.SchemaType, flag.Paginated)
		}
		if surface.PaginatedReason != flag.Reason {
			t.Errorf("surface %q paginated_reason drifted from the single declaration", surface.Name)
		}
		checked++
	}
	if checked < 40 {
		t.Fatalf("expected >=40 surfaces to expose pagination flags, got %d", checked)
	}
}

// TestSchemaTypeListShaped pins the list-shaped classifier the exhaustiveness
// walk depends on.
func TestSchemaTypeListShaped(t *testing.T) {
	type plain struct {
		Name string `json:"name"`
	}
	type withBytes struct {
		Blob []byte `json:"blob"`
	}
	type withList struct {
		Items []string `json:"items"`
	}
	type withIgnoredList struct {
		Items []string `json:"-"`
	}
	cases := []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		{"plain", reflect.TypeOf(plain{}), false},
		{"byte slice is not a JSON array", reflect.TypeOf(withBytes{}), false},
		{"slice field", reflect.TypeOf(withList{}), true},
		{"json-ignored slice", reflect.TypeOf(withIgnoredList{}), false},
		{"pointer to list-shaped", reflect.TypeOf(&withList{}), true},
	}
	for _, tc := range cases {
		if got := SchemaTypeListShaped(tc.typ); got != tc.want {
			t.Errorf("%s: SchemaTypeListShaped=%v want %v", tc.name, got, tc.want)
		}
	}
}
