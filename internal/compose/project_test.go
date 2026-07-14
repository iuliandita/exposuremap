package compose_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	composeparser "github.com/iuliandita/exposuremap/internal/compose"
)

func TestParseProjectMatchesCompatibilityEntryPoints(t *testing.T) {
	directory := t.TempDir()
	path := writeCompose(t, directory, "compose.yaml", `
services:
  web:
    image: nginx
    ports:
      - ${PUBLISHED}:80
`)
	variables := map[string]string{"PUBLISHED": "8080"}

	legacy, err := composeparser.ParseFileWithOptions(
		context.Background(), path, composeparser.ParseOptions{Variables: variables},
	)
	if err != nil {
		t.Fatalf("ParseFileWithOptions() error = %v", err)
	}
	project, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: path, Variables: variables,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if !reflect.DeepEqual(project, legacy) {
		t.Fatalf("ParseProject() = %#v, want compatibility result %#v", project, legacy)
	}
}

func TestParseProjectAppliesOverridesInOrder(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  web:
    image: example/web:base
    ports:
      - 8080:80
`)
	first := writeCompose(t, directory, "compose.first.yaml", `
services:
  web:
    image: example/web:first
`)
	second := writeCompose(t, directory, "compose.second.yaml", `
services:
  web:
    image: example/web:second
    ports: !override
      - 9090:90
`)
	overrides := []string{first, second}

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Overrides: overrides,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	overrides[0], overrides[1] = overrides[1], overrides[0]
	web := serviceNamed(t, revision, "web")
	if web.Image != "example/web:second" {
		t.Fatalf("image = %q, want last override image", web.Image)
	}
	if len(web.Bindings) != 1 || web.Bindings[0].Published != "9090" || web.Bindings[0].Target != 90 {
		t.Fatalf("bindings = %#v, want only last override binding", web.Bindings)
	}
}

func TestParseProjectSupportsResetAndOverrideTags(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  web:
    image: nginx
    ports:
      - 8080:80
    volumes:
      - ./base:/data
`)
	override := writeCompose(t, directory, "compose.override.yaml", `
services:
  web:
    ports: !reset []
    volumes: !override
      - ./head:/data
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Overrides: []string{override},
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	web := serviceNamed(t, revision, "web")
	if len(web.Bindings) != 0 {
		t.Fatalf("bindings = %#v, want reset ports", web.Bindings)
	}
	if len(web.Mounts) != 1 || web.Mounts[0].Source != "./head" {
		t.Fatalf("mounts = %#v, want overridden ./head bind", web.Mounts)
	}
}

func TestParseProjectRequiresExplicitRootForSiblingOverride(t *testing.T) {
	root := t.TempDir()
	appDirectory := filepath.Join(root, "app")
	sharedDirectory := filepath.Join(root, "shared")
	for _, directory := range []string{appDirectory, sharedDirectory} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	primary := writeCompose(t, appDirectory, "compose.yaml", `
services:
  web:
    image: example/web:base
`)
	override := writeCompose(t, sharedDirectory, "compose.override.yaml", `
services:
  web:
    image: example/web:override
`)

	_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Overrides: []string{override},
	})
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("default-root error = %v, want incomplete", err)
	}

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root, Overrides: []string{override},
	})
	if err != nil {
		t.Fatalf("explicit-root ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "web").Image; got != "example/web:override" {
		t.Fatalf("image = %q, want sibling override image", got)
	}
}
