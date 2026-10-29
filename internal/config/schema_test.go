package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateSchema = flag.Bool("update-schema", false, "write testdata/schema-v<Version>.txt from the current Config")

func schemaPaths(prefix string, t reflect.Type, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			emit(path, f.Type, out)
		}
	}
}

func emit(path string, t reflect.Type, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		*out = append(*out, path+" object")
		schemaPaths(path, t, out)
	case reflect.Slice:
		*out = append(*out, path+" array")
		emit(path+"[]", t.Elem(), out)
	case reflect.Map:
		*out = append(*out, path+" map")
		emit(path+"[*]", t.Elem(), out)
	default:
		*out = append(*out, fmt.Sprintf("%s %s", path, t.Kind()))
	}
}

func currentSchema() string {
	var paths []string
	schemaPaths("", reflect.TypeOf(Config{}), &paths)
	sort.Strings(paths)
	return strings.Join(paths, "\n") + "\n"
}

func TestSchemaMatchesVersion(t *testing.T) {
	golden := filepath.Join("testdata", fmt.Sprintf("schema-v%d.txt", Version))
	got := currentSchema()
	if *updateSchema {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("no schema file for config version %d: bump config.Version only with a schema change, then run: go test ./internal/config -run TestSchemaMatchesVersion -update-schema (%v)", Version, err)
	}
	if string(want) != got {
		t.Fatalf("config schema changed without a version bump: bump config.Version, delete the old testdata/schema-v*.txt, and run go test ./internal/config -run TestSchemaMatchesVersion -update-schema\n--- %s\n%s--- current\n%s", golden, want, got)
	}
	others, _ := filepath.Glob(filepath.Join("testdata", "schema-v*.txt"))
	for _, o := range others {
		if o != golden {
			t.Errorf("stale schema file %s: delete it (git keeps the history)", o)
		}
	}
}
