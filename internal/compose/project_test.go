package compose_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestParseProjectRejectsUnsafeEntryPaths(t *testing.T) {
	root := t.TempDir()
	regular := writeCompose(t, root, "regular.yaml", "services: {}\n")
	symlink := filepath.Join(root, "link.yaml")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeCompose(t, realDirectory, "compose.yaml", "services: {}\n")
	ancestorSymlink := filepath.Join(root, "linked")
	if err := os.Symlink("real", ancestorSymlink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	linkedCompose := filepath.Join(ancestorSymlink, "compose.yaml")
	reserved := writeCompose(t, root, ".env", "services: {}\n")

	tests := map[string]composeparser.ProjectInput{
		"directory":        {Primary: root, Root: root},
		"final symlink":    {Primary: symlink, Root: root},
		"ancestor symlink": {Primary: linkedCompose, Root: root},
		"reserved env":     {Primary: reserved, Root: root},
		"missing":          {Primary: filepath.Join(root, "missing.yaml"), Root: root},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := composeparser.ParseProject(context.Background(), input)
			var incomplete *composeparser.IncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("error = %v, want incomplete", err)
			}
		})
	}

	rootSymlink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, rootSymlink); err != nil {
		t.Fatalf("root Symlink() error = %v", err)
	}
	_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: filepath.Join(rootSymlink, "regular.yaml"), Root: rootSymlink,
	})
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("root-symlink error = %v, want incomplete", err)
	}
}

func TestParseProjectRejectsCanceledCaptureWithoutLeakingInput(t *testing.T) {
	root := t.TempDir()
	path := writeCompose(t, root, "DO_NOT_PRINT.yaml", "services: [DO_NOT_PRINT\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := composeparser.ParseProject(ctx, composeparser.ProjectInput{Primary: path, Root: root})
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want incomplete", err)
	}
	for _, forbidden := range []string{root, path, "DO_NOT_PRINT", "exposuremap-compose-"} {
		if strings.Contains(incomplete.Error(), forbidden) {
			t.Fatalf("error leaks %q: %q", forbidden, incomplete.Error())
		}
	}
}
