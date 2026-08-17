package persona

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

const personaSchemaPath = "../../docs/schemas/persona.schema.json"

// schemaObject is the slice of JSON Schema this test walks. Anything else in
// the file (descriptions, patterns, enums) is documentation the drift check
// deliberately does not police.
type schemaObject struct {
	Type                 string                  `json:"type"`
	Required             []string                `json:"required"`
	AdditionalProperties *bool                   `json:"additionalProperties"`
	Properties           map[string]schemaObject `json:"properties"`
	Enum                 []string                `json:"enum"`
	Const                *string                 `json:"const"`
}

func loadPersonaSchema(t *testing.T) schemaObject {
	t.Helper()
	data, err := os.ReadFile(personaSchemaPath)
	if err != nil {
		t.Fatalf("reading persona.schema.json: %v", err)
	}
	var schema schemaObject
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("persona.schema.json is not valid JSON: %v", err)
	}
	return schema
}

// yamlFields returns the yaml key of every serialized field of a struct type,
// paired with the field type so nested objects can be walked. Fields tagged
// yaml:"-" are not on disk and so are not in the schema.
func yamlFields(t *testing.T, typ reflect.Type) map[string]reflect.Type {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("yamlFields(%s): not a struct", typ)
	}
	out := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = field.Type
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertObjectMatches compares one struct against one schema object in both
// directions. A field with no property means the schema is stale; a property
// with no field means the schema documents something that cannot be written.
// additionalProperties must stay false so a typo in a persona.yaml is caught
// by an editor rather than silently ignored.
func assertObjectMatches(t *testing.T, path string, typ reflect.Type, obj schemaObject) {
	t.Helper()
	if obj.AdditionalProperties == nil || *obj.AdditionalProperties {
		t.Errorf("%s: additionalProperties must be false so unknown keys are a hard failure", path)
	}
	fields := yamlFields(t, typ)
	for name, fieldType := range fields {
		prop, ok := obj.Properties[name]
		if !ok {
			t.Errorf("%s: struct field %q has no schema property (schema has %v)", path, name, sortedKeys(obj.Properties))
			continue
		}
		if fieldType.Kind() == reflect.Struct {
			assertObjectMatches(t, path+"."+name, fieldType, prop)
		}
	}
	for name := range obj.Properties {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s: schema property %q has no struct field (struct has %v)", path, name, sortedKeys(fields))
		}
	}
}

// TestPersonaSchemaMatchesProfile fails on any divergence in either direction
// between internal/persona.Profile and docs/schemas/persona.schema.json. The
// schema was stale for exactly this reason: brain and tts.tier shipped in the
// struct and never reached the file (G34).
func TestPersonaSchemaMatchesProfile(t *testing.T) {
	assertObjectMatches(t, "persona", reflect.TypeOf(Profile{}), loadPersonaSchema(t))
}

// The document is versioned; the schema has to name the version it describes.
func TestPersonaSchemaPinsSchemaConst(t *testing.T) {
	schema := loadPersonaSchema(t)
	prop, ok := schema.Properties["schema"]
	if !ok {
		t.Fatal("schema property missing")
	}
	if prop.Const == nil || *prop.Const != Schema {
		t.Fatalf("schema const = %v, want %q", prop.Const, Schema)
	}
}

// A tier the schema allows but the validator rejects would send an editor's
// user down a path the writer refuses.
func TestPersonaSchemaTierEnumMatchesValidator(t *testing.T) {
	schema := loadPersonaSchema(t)
	tier := schema.Properties["tts"].Properties["tier"]
	if len(tier.Enum) == 0 {
		t.Fatal("tts.tier has no enum")
	}
	for _, value := range tier.Enum {
		if err := ValidateTier(value); err != nil {
			t.Errorf("schema allows tts.tier %q but the validator rejects it: %v", value, err)
		}
	}
	for _, value := range []string{"", managedqwen.DefaultModelTier, managedqwen.Tier1_7B} {
		if !containsString(tier.Enum, value) {
			t.Errorf("validator accepts tts.tier %q but the schema enum %v does not list it", value, tier.Enum)
		}
	}
}

// Every required key must be a real field, or a valid document fails schema
// validation for a property nothing can supply.
func TestPersonaSchemaRequiredKeysExist(t *testing.T) {
	schema := loadPersonaSchema(t)
	fields := yamlFields(t, reflect.TypeOf(Profile{}))
	for _, name := range schema.Required {
		if _, ok := fields[name]; !ok {
			t.Errorf("schema requires %q, which is not a Profile field", name)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
