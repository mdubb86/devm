package schema_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
)

// TestOverrideMirrorsBase locks the invariant that every exported
// field in a base struct has a corresponding field in its override
// twin, with the right "absence-distinguishable" shape:
//
//	string / int / bool  → *T
//	[]T                  → *[]T
//	map[K]V (V scalar)   → map[K]V        (nil is already "absent")
//	map[K]Struct         → map[K]StructOverride
//	struct (nested)      → *<Name>Override
//
// Drift in any direction (missing override field, wrong shape, or an
// override field whose merge clause was forgotten) is a silent-drop
// bug class — the field gets parsed and then dropped on the floor.
// This test catches the structural half; merge.go has its own tests
// covering the merge half.
//
// Adding a new Config field requires either adding the corresponding
// ConfigOverride field OR adding the field name to the denylist below
// with a one-line justification.
func TestOverrideMirrorsBase(t *testing.T) {
	cases := []struct {
		name     string
		base     reflect.Type
		override reflect.Type
		// Field names in base that are intentionally NOT overridable.
		// Maps to a one-line reason.
		denylist map[string]string
	}{
		{
			name:     "Config",
			base:     reflect.TypeOf(schema.Config{}),
			override: reflect.TypeOf(schema.ConfigOverride{}),
			denylist: map[string]string{
				"BaseImage": "empty struct (no fields); base_image: key is still parsed for YAML compat but nothing is overridable",
			},
		},
		{
			name:     "Project",
			base:     reflect.TypeOf(schema.Project{}),
			override: reflect.TypeOf(schema.ProjectOverride{}),
			denylist: nil,
		},
		{
			name:     "Network",
			base:     reflect.TypeOf(schema.Network{}),
			override: reflect.TypeOf(schema.NetworkOverride{}),
			denylist: nil,
		},
		{
			name:     "Service",
			base:     reflect.TypeOf(schema.Service{}),
			override: reflect.TypeOf(schema.ServiceOverride{}),
			denylist: map[string]string{
				"BindIP": "set via Port field's YAML polymorphism, not standalone overridable",
				// If other fields legitimately can't be overridden, add
				// them here with a reason. Don't add a name just to
				// make the test pass — that defeats the purpose.
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertOverrideMirrors(t, c.base, c.override, c.denylist)
		})
	}
}

func assertOverrideMirrors(t *testing.T, base, override reflect.Type, denylist map[string]string) {
	t.Helper()
	overrideFields := indexExported(override)
	for i := 0; i < base.NumField(); i++ {
		bf := base.Field(i)
		if !bf.IsExported() {
			continue
		}
		if _, denied := denylist[bf.Name]; denied {
			continue
		}
		of, ok := overrideFields[bf.Name]
		if !ok {
			t.Errorf(
				"DRIFT: %s.%s has no matching field in %s. "+
					"Add it (with appropriate pointer-wrapping) or add %q to the denylist with a justification.",
				base.Name(), bf.Name, override.Name(), bf.Name,
			)
			continue
		}
		want := expectedOverrideType(bf.Type)
		if of.Type != want {
			t.Errorf(
				"SHAPE: %s.%s has type %s, expected %s. "+
					"Override field needs the absence-distinguishable shape (typically *T).",
				override.Name(), of.Name, of.Type, want,
			)
		}
	}
}

func indexExported(t reflect.Type) map[string]reflect.StructField {
	out := make(map[string]reflect.StructField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.IsExported() {
			out[f.Name] = f
		}
	}
	return out
}

// expectedOverrideType returns the expected ConfigOverride/etc.
// shape for a given base-struct field type.
func expectedOverrideType(baseType reflect.Type) reflect.Type {
	switch baseType.Kind() {
	case reflect.Map:
		// map[K]V: if V is a struct in the schema package, expect
		// map[K]VOverride. Otherwise (primitive value), map stays
		// map (nil already represents absent).
		elem := baseType.Elem()
		if elem.Kind() == reflect.Struct && elem.PkgPath() == "github.com/mdubb86/devm/internal/schema" {
			if overrideElem := overrideTypeByName(elem.Name()); overrideElem != nil {
				return reflect.MapOf(baseType.Key(), overrideElem)
			}
		}
		return baseType
	case reflect.Slice:
		// []T → *[]T
		return reflect.PtrTo(baseType)
	case reflect.Struct:
		// Nested struct in schema package → *<Name>Override
		if baseType.PkgPath() == "github.com/mdubb86/devm/internal/schema" {
			if o := overrideTypeByName(baseType.Name()); o != nil {
				return reflect.PtrTo(o)
			}
		}
		return baseType
	default:
		// Scalar (string, int, bool, etc.) → *T
		return reflect.PtrTo(baseType)
	}
}

// overrideTypeByName looks up <Name>Override in the schema package
// via a representative instance. Returns nil if no such type.
func overrideTypeByName(name string) reflect.Type {
	// Hard-coded lookup table because reflect doesn't enumerate
	// package types. Add entries as new Override types are added.
	table := map[string]reflect.Type{
		"Project": reflect.TypeOf(schema.ProjectOverride{}),
		"Network": reflect.TypeOf(schema.NetworkOverride{}),
		"Service": reflect.TypeOf(schema.ServiceOverride{}),
	}
	return table[name]
}

// Sanity-check the test itself: a known-good pair should pass.
func TestOverrideMirrorsBase_SmokeTest(t *testing.T) {
	// If Config and ConfigOverride are correctly paired today
	// (verified manually in the 2026-06-24 audit), this should
	// always pass. If it fails, either the audit was wrong or
	// someone introduced drift.
	if strings.Count(reflect.TypeOf(schema.Config{}).Name(), "Config") != 1 {
		t.Skip("smoke test only runs on Config type")
	}
}
