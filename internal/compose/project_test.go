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

func TestParseProjectIncludesShortAndNestedProjects(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
include:
  - apps/compose.yaml
services:
  gateway:
    image: example/gateway:1
`)
	writeProjectCompose(t, root, "apps/compose.yaml", `
include:
  - worker.yaml
services:
  web:
    image: example/web:1
    ports:
      - 8080:80
`)
	writeProjectCompose(t, root, "apps/worker.yaml", `
services:
  worker:
    image: example/worker:1
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	for _, name := range []string{"gateway", "web", "worker"} {
		serviceNamed(t, revision, name)
	}
	web := serviceNamed(t, revision, "web")
	if len(web.Bindings) != 1 || web.Bindings[0].Published != "8080" {
		t.Fatalf("web bindings = %#v, want included port", web.Bindings)
	}
}

func TestParseProjectIncludesOrderedPathList(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
include:
  - path:
      - stack/compose.yaml
      - stack/compose.override.yaml
`)
	writeProjectCompose(t, root, "stack/compose.yaml", `
services:
  web:
    image: example/web:base
    ports:
      - 8080:80
`)
	writeProjectCompose(t, root, "stack/compose.override.yaml", `
services:
  web:
    image: example/web:override
    ports: !override
      - 9090:90
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	web := serviceNamed(t, revision, "web")
	if web.Image != "example/web:override" || len(web.Bindings) != 1 || web.Bindings[0].Published != "9090" {
		t.Fatalf("web = %#v, want ordered include override", web)
	}
}

func TestParseProjectIncludeUsesProjectDirectoryAndVariables(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
include:
  - path: configs/compose.yaml
    project_directory: runtime
`)
	writeProjectCompose(t, root, "configs/compose.yaml", `
services:
  web:
    image: ${IMAGE}
    ports:
      - ${PUBLISHED}:80
    volumes:
      - ./data:/data
`)
	variables := map[string]string{"IMAGE": "example/web:1", "PUBLISHED": "8080"}

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root, Variables: variables,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	variables["IMAGE"] = "example/web:changed"
	web := serviceNamed(t, revision, "web")
	if web.Image != "example/web:1" || len(web.Bindings) != 1 || web.Bindings[0].Published != "8080" {
		t.Fatalf("web = %#v, want controlled included boundary facts", web)
	}
	if len(web.Mounts) != 1 || filepath.ToSlash(web.Mounts[0].Source) != "runtime/data" {
		t.Fatalf("mounts = %#v, want project-directory-relative bind", web.Mounts)
	}
}

func TestParseProjectIncludesFromExplicitOverride(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", "services: {}\n")
	override := writeProjectCompose(t, root, "compose.override.yaml", "include:\n  - child.yaml\n")
	writeProjectCompose(t, root, "child.yaml", "services:\n  worker:\n    image: example/worker:1\n")

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root, Overrides: []string{override},
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	serviceNamed(t, revision, "worker")
}

func TestParseProjectMergesIncludeConflictUsingComposeRules(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
include:
  - child.yaml
services:
  web:
    image: example/web:primary
`)
	writeProjectCompose(t, root, "child.yaml", "services:\n  web:\n    image: example/web:child\n")

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "web").Image; got != "example/web:primary" {
		t.Fatalf("image = %q, want current-project value", got)
	}
}

func TestParseProjectRejectsUnsafeIncludes(t *testing.T) {
	tests := map[string]string{
		"absolute":          "include:\n  - /tmp/compose.yaml\n",
		"traversal":         "include:\n  - ../compose.yaml\n",
		"remote URL":        "include:\n  - https://example.invalid/compose.yaml\n",
		"unbraced variable": "include:\n  - $CONFIG\n",
		"braced variable":   "include:\n  - ${CONFIG}\n",
		"escaped dollar":    "include:\n  - $$CONFIG\n",
		"missing path":      "include:\n  - project_directory: app\n",
		"empty path":        "include:\n  - path: ''\n",
		"non-string path":   "include:\n  - path: 42\n",
		"unknown key":       "include:\n  - path: child.yaml\n    unknown: true\n",
		"env file":          "include:\n  - path: child.yaml\n    env_file: []\n",
		"null env file":     "include:\n  - path: child.yaml\n    env_file: /dev/null\n",
		"project traversal": "include:\n  - path: child.yaml\n    project_directory: ../outside\n",
		"reserved env":      "include:\n  - .env\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			primary := writeProjectCompose(t, root, "compose.yaml", contents)
			writeProjectCompose(t, root, "child.yaml", "services: {}\n")
			writeProjectCompose(t, root, ".env", "DO_NOT_PRINT=1\n")

			_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
				Primary: primary, Root: root,
			})
			var incomplete *composeparser.IncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("error = %v, want incomplete", err)
			}
			for _, forbidden := range []string{root, "DO_NOT_PRINT", "exposuremap-compose-"} {
				if strings.Contains(incomplete.Error(), forbidden) {
					t.Fatalf("error leaks %q: %q", forbidden, incomplete.Error())
				}
			}
		})
	}
}

func TestParseProjectRejectsIncludeSymlinksAndCycles(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		primary := writeProjectCompose(t, root, "compose.yaml", "include:\n  - linked.yaml\n")
		target := writeProjectCompose(t, root, "target.yaml", "services: {}\n")
		if err := os.Symlink(target, filepath.Join(root, "linked.yaml")); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		assertIncomplete(t, err)
	})

	t.Run("cycle", func(t *testing.T) {
		root := t.TempDir()
		primary := writeProjectCompose(t, root, "compose.yaml", "include:\n  - child.yaml\n")
		writeProjectCompose(t, root, "child.yaml", "include:\n  - compose.yaml\n")
		_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		assertIncomplete(t, err)
	})
}

func writeProjectCompose(t *testing.T, root, relativePath, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	return writeCompose(t, filepath.Dir(path), filepath.Base(path), contents)
}
