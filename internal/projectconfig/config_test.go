package projectconfig

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadMergesCommonAndRevisionVariables(t *testing.T) {
	path := writeContext(t, "context.yml", `schema_version: 1
variables:
  common:
    DATA_ROOT: ./data
    EMPTY: ""
  base:
    WEB_PORT: "8080"
  head:
    WEB_PORT: "8081"
`)

	variables, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for name, want := range map[string]string{"DATA_ROOT": "./data", "EMPTY": "", "WEB_PORT": "8080"} {
		if got := variables.Base[name]; got != want {
			t.Errorf("Base[%q] = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{"DATA_ROOT": "./data", "EMPTY": "", "WEB_PORT": "8081"} {
		if got := variables.Head[name]; got != want {
			t.Errorf("Head[%q] = %q, want %q", name, got, want)
		}
	}

	variables.Base["DATA_ROOT"] = "changed"
	if variables.Head["DATA_ROOT"] != "./data" {
		t.Fatalf("Base and Head maps alias: %#v", variables)
	}
}

func TestLoadRejectsInvalidDocumentsWithoutLeakingValues(t *testing.T) {
	tooMany := strings.Builder{}
	tooMany.WriteString("schema_version: 1\nvariables:\n  common:\n")
	for index := range 257 {
		tooMany.WriteString("    VARIABLE_")
		tooMany.WriteString(strconv.Itoa(index))
		tooMany.WriteString(": value\n")
	}

	tests := map[string]string{
		"missing schema":     `variables: {}`,
		"future schema":      `schema_version: 2`,
		"unknown field":      "schema_version: 1\nunknown: DO_NOT_PRINT\n",
		"duplicate key":      "schema_version: 1\nvariables:\n  common:\n    PORT: one\n    PORT: DO_NOT_PRINT\n",
		"non-string value":   "schema_version: 1\nvariables:\n  common:\n    PORT: 8080\n",
		"invalid name":       "schema_version: 1\nvariables:\n  common:\n    1INVALID: DO_NOT_PRINT\n",
		"multiple documents": "schema_version: 1\n---\nschema_version: 1\n",
		"too many variables": tooMany.String(),
		"value too long":     "schema_version: 1\nvariables:\n  common:\n    VALUE: " + strings.Repeat("x", 4097) + "\n",
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeContext(t, "context.yml", contents)
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load() error = nil, want rejection")
			}
			for _, forbidden := range []string{filepath.Dir(path), "DO_NOT_PRINT", strings.Repeat("x", 64)} {
				if forbidden != "" && strings.Contains(err.Error(), forbidden) {
					t.Fatalf("Load() error leaks input %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestLoadRejectsUnsafeFilesWithoutLeakingPaths(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yml")
	if err := os.WriteFile(target, []byte("schema_version: 1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	symlink := filepath.Join(directory, "context.yml")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	oversized := filepath.Join(directory, "oversized.yml")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", (64<<10)+1)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for name, path := range map[string]string{
		"directory": directory,
		"missing":   filepath.Join(directory, "missing.yml"),
		"oversized": oversized,
		"symlink":   symlink,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load() error = nil, want rejection")
			}
			if strings.Contains(err.Error(), directory) {
				t.Fatalf("Load() error leaks local directory: %v", err)
			}
		})
	}
}

func writeContext(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
