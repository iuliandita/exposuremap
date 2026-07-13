package compose_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	composeparser "github.com/iuliandita/exposuremap/internal/compose"
	"github.com/iuliandita/exposuremap/internal/graph"
)

func TestParseFileNormalizesSupportedBoundaryFacts(t *testing.T) {
	revision, err := composeparser.ParseFile(context.Background(), "testdata/supported.yaml")
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if !revision.Complete {
		t.Fatal("revision.Complete = false, want true")
	}
	if len(revision.Services) != 4 {
		t.Fatalf("services = %#v, want four", revision.Services)
	}

	database := serviceNamed(t, revision, "database")
	if !slices.Equal(database.Networks, []string{"default"}) || database.Reachability != graph.ReachabilityInternal {
		t.Fatalf("database = %#v, want implicit internal default network", database)
	}
	host := serviceNamed(t, revision, "host")
	if !host.HostNetwork || host.Reachability != graph.ReachabilityHostExposed {
		t.Fatalf("host = %#v, want host-exposed host mode", host)
	}
	web := serviceNamed(t, revision, "web")
	if len(web.Bindings) != 2 {
		t.Fatalf("web bindings = %#v, want two", web.Bindings)
	}
	if web.Bindings[0].Published != "8080" || web.Bindings[0].Target != 80 || web.Bindings[0].Protocol != "tcp" {
		t.Fatalf("short binding = %#v", web.Bindings[0])
	}
	if web.Bindings[1].HostIP != "127.0.0.1" || web.Bindings[1].Published != "8443" || web.Bindings[1].Target != 443 {
		t.Fatalf("long binding = %#v", web.Bindings[1])
	}
	if len(web.Mounts) != 1 || !web.Mounts[0].Sensitive || !web.Mounts[0].ReadOnly {
		t.Fatalf("web mounts = %#v, want read-only sensitive bind", web.Mounts)
	}
	worker := serviceNamed(t, revision, "worker")
	if !worker.Privileged || len(worker.Mounts) != 1 || worker.Mounts[0].Type != "bind" {
		t.Fatalf("worker = %#v, want privileged with bind mount", worker)
	}
}

func TestParseFileRejectsNonRegularAndOversizedInputs(t *testing.T) {
	directory := t.TempDir()
	target := writeCompose(t, directory, "target.yaml", "services:\n  web:\n    image: nginx\n")
	symlink := filepath.Join(directory, "link.yaml")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	for name, candidate := range map[string]string{
		"directory": directory,
		"symlink":   symlink,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := composeparser.ParseFile(context.Background(), candidate)
			assertIncomplete(t, err)
		})
	}

	oversized := filepath.Join(directory, "oversized.yaml")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := composeparser.ParseFile(context.Background(), oversized)
	assertIncomplete(t, err)
}

func TestParseFileRejectsUnsupportedExternalInputs(t *testing.T) {
	tests := map[string]string{
		"include":    "include:\n  - other.yaml\nservices:\n  web:\n    image: nginx\n",
		"extends":    "services:\n  web:\n    extends:\n      file: other.yaml\n      service: base\n",
		"label file": "services:\n  web:\n    image: nginx\n    label_file: labels.env\n",
		"profiles":   "services:\n  web:\n    image: nginx\n    profiles: [debug]\n",
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeCompose(t, t.TempDir(), "compose.yaml", contents)
			_, err := composeparser.ParseFile(context.Background(), path)
			assertIncomplete(t, err)
		})
	}
}

func TestParseFileIgnoresServiceEnvFilesWithoutReadingThem(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  long:
    image: nginx
    env_file:
      - path: missing-long.env
        required: true
    ports:
      - "127.0.0.1:8081:80"
  short:
    image: nginx
    env_file: missing-short.env
    ports:
      - "127.0.0.1:8080:80"
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if !revision.Complete {
		t.Fatal("revision.Complete = false, want true")
	}
	for serviceName, published := range map[string]string{"long": "8081", "short": "8080"} {
		service := serviceNamed(t, revision, serviceName)
		if len(service.Bindings) != 1 || service.Bindings[0].Published != published {
			t.Fatalf("%s bindings = %#v, want published port %s", serviceName, service.Bindings, published)
		}
	}
}

func TestParseFileRejectsMalformedOrUnresolvedInput(t *testing.T) {
	tests := map[string]string{
		"malformed":               "services: [",
		"unresolved env file":     "services:\n  web:\n    image: nginx\n    env_file: ${ENV_FILE:?required}\n",
		"unresolved publish port": "services:\n  web:\n    image: nginx\n    ports:\n      - ${PUBLISHED:?required}:80\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeCompose(t, t.TempDir(), "compose.yaml", contents)
			_, err := composeparser.ParseFile(context.Background(), path)
			assertIncomplete(t, err)
		})
	}
}

func TestParseFileUsesStableLoaderDiagnostic(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  first:
    image: postgres
    volumes:
      - ${FIRST_PATH}:/data
  second:
    image: postgres
    volumes:
      - ${SECOND_PATH}:/var/lib/postgresql/data
`)

	_, err := composeparser.ParseFile(context.Background(), path)

	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *compose.IncompleteError", err)
	}
	if got, want := incomplete.Error(), "parse compose file: invalid or unresolved Compose model"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func serviceNamed(t *testing.T, revision graph.Revision, name string) graph.Service {
	t.Helper()
	for _, service := range revision.Services {
		if service.Name == name {
			return service
		}
	}
	t.Fatalf("service %q not found in %#v", name, revision.Services)
	return graph.Service{}
}

func writeCompose(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func assertIncomplete(t *testing.T, err error) {
	t.Helper()
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *compose.IncompleteError", err)
	}
	if incomplete.Error() == "" {
		t.Fatal("incomplete error is empty")
	}
}
