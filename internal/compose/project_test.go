package compose_test

import (
	"context"
	"errors"
	"fmt"
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

	t.Run("project directory symlink", func(t *testing.T) {
		root := t.TempDir()
		primary := writeProjectCompose(t, root, "compose.yaml", `
include:
  - path: child.yaml
    project_directory: linked
`)
		writeProjectCompose(t, root, "child.yaml", "services: {}\n")
		if err := os.Mkdir(filepath.Join(root, "real"), 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.Symlink("real", filepath.Join(root, "linked")); err != nil {
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

func TestParseProjectExtendsSameFileService(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
services:
  base:
    image: example/base:1
    privileged: true
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
  web:
    extends: base
    image: example/web:1
    ports:
      - 8080:80
  long:
    extends:
      service: base
    privileged: false
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	web := serviceNamed(t, revision, "web")
	if web.Image != "example/web:1" || !web.Privileged || len(web.Bindings) != 1 ||
		web.Bindings[0].Published != "8080" || len(web.Mounts) != 1 {
		t.Fatalf("web = %#v, want same-file inherited facts", web)
	}
	long := serviceNamed(t, revision, "long")
	if long.Image != "example/base:1" || long.Privileged || len(long.Mounts) != 1 {
		t.Fatalf("long = %#v, want inherited facts and privileged override", long)
	}
}

func TestParseProjectExtendsRecursiveExternalServices(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
services:
  web:
    extends:
      file: bases/base.yaml
      service: middle
    ports:
      - ${PUBLISHED}:80
`)
	writeProjectCompose(t, root, "bases/base.yaml", `
services:
  middle:
    extends:
      file: nested/root.yaml
      service: root
    privileged: true
`)
	writeProjectCompose(t, root, "bases/nested/root.yaml", `
services:
  root:
    image: ${IMAGE}
    volumes:
      - ./state:/state
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary,
		Root:    root,
		Variables: map[string]string{
			"IMAGE": "example/web:1", "PUBLISHED": "8080",
		},
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	web := serviceNamed(t, revision, "web")
	if web.Image != "example/web:1" || !web.Privileged || len(web.Bindings) != 1 ||
		web.Bindings[0].Published != "8080" || len(web.Mounts) != 1 {
		t.Fatalf("web = %#v, want recursive inherited facts", web)
	}
}

func TestParseProjectExtendsInOverrideFromProjectDirectory(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", "services: {}\n")
	override := writeProjectCompose(t, root, "overrides/compose.yaml", `
services:
  worker:
    extends:
      file: bases.yaml
      service: base
`)
	writeProjectCompose(t, root, "bases.yaml", "services:\n  base:\n    image: example/worker:1\n")

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root, Overrides: []string{override},
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "worker").Image; got != "example/worker:1" {
		t.Fatalf("image = %q, want project-directory external base", got)
	}
}

func TestParseProjectExtendsDoesNotImportReferencedResources(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
services:
  web:
    extends:
      file: base.yaml
      service: base
`)
	writeProjectCompose(t, root, "base.yaml", `
services:
  base:
    image: nginx
    networks: [backend]
networks:
  backend: {}
`)

	_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	assertIncomplete(t, err)
}

func TestParseProjectExtendsIgnoresUnusedServiceImports(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
services:
  web:
    extends:
      file: base.yaml
      service: wanted
`)
	writeProjectCompose(t, root, "base.yaml", `
services:
  wanted:
    image: example/web:1
  unused:
    extends:
      file: ../outside.yaml
      service: unsafe
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "web").Image; got != "example/web:1" {
		t.Fatalf("image = %q, want requested external service", got)
	}
}

func TestParseProjectRejectsUnsafeExtends(t *testing.T) {
	tests := map[string]string{
		"absolute":          "/tmp/base.yaml",
		"traversal":         "../base.yaml",
		"remote":            "https://example.invalid/base.yaml",
		"unbraced variable": "$BASE_FILE",
		"braced variable":   "${BASE_FILE}",
		"escaped dollar":    "$$BASE_FILE",
		"reserved env":      ".env",
	}
	for name, reference := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			primary := writeProjectCompose(t, root, "compose.yaml", fmt.Sprintf(`
services:
  web:
    extends:
      file: %q
      service: base
`, reference))
			writeProjectCompose(t, root, ".env", "DO_NOT_PRINT=1\n")

			_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
				Primary: primary, Root: root,
			})
			var incomplete *composeparser.IncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("error = %v, want incomplete", err)
			}
			for _, forbidden := range []string{root, reference, "DO_NOT_PRINT", "exposuremap-compose-"} {
				if strings.Contains(incomplete.Error(), forbidden) {
					t.Fatalf("error leaks %q: %q", forbidden, incomplete.Error())
				}
			}
		})
	}
}

func TestParseProjectRejectsMalformedOrMissingExtends(t *testing.T) {
	tests := map[string]string{
		"missing service": "services:\n  web:\n    extends:\n      file: base.yaml\n",
		"invalid mapping": "services:\n  web:\n    extends:\n      service: 42\n",
		"unknown service": "services:\n  web:\n    extends: missing\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			primary := writeProjectCompose(t, root, "compose.yaml", contents)
			writeProjectCompose(t, root, "base.yaml", "services: {}\n")
			_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
				Primary: primary, Root: root,
			})
			assertIncomplete(t, err)
		})
	}
}

func TestParseProjectRejectsExtendsSymlinkAndCycles(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		primary := writeProjectCompose(t, root, "compose.yaml", `
services:
  web:
    extends:
      file: linked.yaml
      service: base
`)
		target := writeProjectCompose(t, root, "target.yaml", "services:\n  base:\n    image: nginx\n")
		if err := os.Symlink(target, filepath.Join(root, "linked.yaml")); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		assertIncomplete(t, err)
	})

	t.Run("same file cycle", func(t *testing.T) {
		root := t.TempDir()
		primary := writeProjectCompose(t, root, "compose.yaml", `
services:
  first:
    extends: second
  second:
    extends: first
`)
		_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		assertIncomplete(t, err)
	})

	t.Run("external cycle", func(t *testing.T) {
		root := t.TempDir()
		primary := writeProjectCompose(t, root, "compose.yaml", `
services:
  web:
    extends:
      file: first.yaml
      service: first
`)
		writeProjectCompose(t, root, "first.yaml", `
services:
  first:
    extends:
      file: second.yaml
      service: second
`)
		writeProjectCompose(t, root, "second.yaml", `
services:
  second:
    extends:
      file: first.yaml
      service: first
`)
		_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		assertIncomplete(t, err)
	})
}

func TestParseProjectLimitsFilesAndBytes(t *testing.T) {
	t.Run("64 files", func(t *testing.T) {
		root := t.TempDir()
		paths := make([]string, 63)
		for index := range paths {
			paths[index] = fmt.Sprintf("files/%02d.yaml", index)
			writeProjectCompose(t, root, paths[index], "services: {}\n")
		}
		primary := writeProjectCompose(t, root, "compose.yaml", includePathList(paths))

		revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		if err != nil || !revision.Complete {
			t.Fatalf("64-file ParseProject() revision = %#v, error = %v", revision, err)
		}
	})

	t.Run("65 files", func(t *testing.T) {
		root := t.TempDir()
		paths := make([]string, 64)
		for index := range paths {
			paths[index] = fmt.Sprintf("files/%02d.yaml", index)
			writeProjectCompose(t, root, paths[index], "services: {}\n")
		}
		primary := writeProjectCompose(t, root, "compose.yaml", includePathList(paths))
		_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		assertIncomplete(t, err)
	})

	t.Run("one MiB", func(t *testing.T) {
		root := t.TempDir()
		primary := writeProjectCompose(t, root, "compose.yaml", paddedCompose("services: {}\n", 1<<20))
		revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		if err != nil || !revision.Complete {
			t.Fatalf("1-MiB ParseProject() revision = %#v, error = %v", revision, err)
		}

		oversized := writeProjectCompose(t, root, "oversized.yaml", paddedCompose("services: {}\n", (1<<20)+1))
		_, err = composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: oversized, Root: root,
		})
		assertIncomplete(t, err)
	})

	t.Run("eight MiB total", func(t *testing.T) {
		root := t.TempDir()
		paths := make([]string, 7)
		for index := range paths {
			paths[index] = fmt.Sprintf("files/%02d.yaml", index)
			writeProjectCompose(t, root, paths[index], paddedCompose("services: {}\n", 1<<20))
		}
		primary := writeProjectCompose(t, root, "compose.yaml", paddedCompose(includePathList(paths), 1<<20))
		revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: primary, Root: root,
		})
		if err != nil || !revision.Complete {
			t.Fatalf("8-MiB ParseProject() revision = %#v, error = %v", revision, err)
		}

		extra := writeProjectCompose(t, root, "files/extra.yaml", "#\n")
		paths = append(paths, filepath.ToSlash(strings.TrimPrefix(extra, root+string(os.PathSeparator))))
		overPrimary := writeProjectCompose(t, root, "over.yaml", paddedCompose(includePathList(paths), 1<<20))
		_, err = composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
			Primary: overPrimary, Root: root,
		})
		assertIncomplete(t, err)
	})
}

func TestParseProjectLimitsImportDepth(t *testing.T) {
	tests := []struct {
		name    string
		write   func(*testing.T, string, int) string
		edges   int
		wantErr bool
	}{
		{name: "include 16", write: writeIncludeDepthProject, edges: 16},
		{name: "include 17", write: writeIncludeDepthProject, edges: 17, wantErr: true},
		{name: "extends 16", write: writeExtendsDepthProject, edges: 16},
		{name: "extends 17", write: writeExtendsDepthProject, edges: 17, wantErr: true},
		{name: "mixed 16", write: writeMixedDepthProject, edges: 16},
		{name: "mixed 17", write: writeMixedDepthProject, edges: 17, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			primary := test.write(t, root, test.edges)
			revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
				Primary: primary, Root: root,
			})
			if test.wantErr {
				assertIncomplete(t, err)
				return
			}
			if err != nil || !revision.Complete {
				t.Fatalf("ParseProject() revision = %#v, error = %v", revision, err)
			}
		})
	}
}

func TestParseProjectIgnoresAmbientInputsInIncludes(t *testing.T) {
	t.Setenv("IMAGE", "ambient.invalid/image")
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", "include:\n  - included/compose.yaml\n")
	writeProjectCompose(t, root, "included/compose.yaml", `
services:
  web:
    image: ${IMAGE}
`)
	writeProjectCompose(t, root, "included/.env", "IMAGE=dotenv.invalid/image\nDO_NOT_PRINT=1\n")

	_, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want incomplete", err)
	}
	for _, forbidden := range []string{"ambient.invalid", "dotenv.invalid", "DO_NOT_PRINT", root} {
		if strings.Contains(incomplete.Error(), forbidden) {
			t.Fatalf("error leaks %q: %q", forbidden, incomplete.Error())
		}
	}
}

func TestParseProjectIgnoresImportedServiceEnvFile(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", "include:\n  - included/compose.yaml\n")
	writeProjectCompose(t, root, "included/compose.yaml", `
services:
  web:
    image: nginx
    env_file: missing.env
    ports:
      - 8080:80
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "web").Bindings[0].Published; got != "8080" {
		t.Fatalf("published = %q, want 8080", got)
	}
}

func TestParseProjectNormalizesImportedPaths(t *testing.T) {
	root := t.TempDir()
	primary := writeProjectCompose(t, root, "compose.yaml", `
include:
  - included/compose.yaml
services:
  primary:
    image: nginx
    volumes:
      - ./primary:/primary
      - /srv/absolute:/absolute
  external:
    extends:
      file: bases/base.yaml
      service: base
`)
	override := writeProjectCompose(t, root, "overrides/compose.yaml", `
services:
  override:
    image: nginx
    volumes:
      - ./override:/override
`)
	writeProjectCompose(t, root, "included/compose.yaml", `
services:
  included:
    image: nginx
    volumes:
      - ./data:/data
`)
	writeProjectCompose(t, root, "bases/base.yaml", `
services:
  base:
    image: nginx
    volumes:
      - ./state:/state
`)

	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root, Overrides: []string{override},
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	wants := map[string][]string{
		"primary":  {"./primary", "/srv/absolute"},
		"override": {"./override"},
		"included": {"included/data"},
		"external": {"bases/state"},
	}
	for serviceName, sources := range wants {
		service := serviceNamed(t, revision, serviceName)
		if len(service.Mounts) != len(sources) {
			t.Fatalf("%s mounts = %#v, want sources %#v", serviceName, service.Mounts, sources)
		}
		for index, source := range sources {
			got := filepath.ToSlash(service.Mounts[index].Source)
			if got != source || strings.Contains(got, "exposuremap-compose-") || strings.Contains(got, root) {
				t.Fatalf("%s mount %d source = %q, want %q", serviceName, index, got, source)
			}
		}
	}
}

func includePathList(paths []string) string {
	var result strings.Builder
	result.WriteString("include:\n  - path:\n")
	for _, path := range paths {
		fmt.Fprintf(&result, "      - %s\n", filepath.ToSlash(path))
	}
	result.WriteString("services: {}\n")
	return result.String()
}

func paddedCompose(contents string, size int) string {
	padding := size - len(contents) - len("#\n")
	if padding < 0 {
		panic("compose content exceeds requested size")
	}
	return contents + "#" + strings.Repeat("x", padding) + "\n"
}

func writeIncludeDepthProject(t *testing.T, root string, edges int) string {
	for level := range edges + 1 {
		contents := "services: {}\n"
		if level < edges {
			contents = fmt.Sprintf("include:\n  - level-%02d.yaml\nservices: {}\n", level+1)
		}
		writeProjectCompose(t, root, fmt.Sprintf("level-%02d.yaml", level), contents)
	}
	return filepath.Join(root, "level-00.yaml")
}

func writeExtendsDepthProject(t *testing.T, root string, edges int) string {
	return writeProjectCompose(t, root, "compose.yaml", extendsChain(edges))
}

func writeMixedDepthProject(t *testing.T, root string, edges int) string {
	primary := writeProjectCompose(t, root, "compose.yaml", "include:\n  - child.yaml\n")
	writeProjectCompose(t, root, "child.yaml", extendsChain(edges-1))
	return primary
}

func extendsChain(edges int) string {
	var result strings.Builder
	result.WriteString("services:\n")
	for level := range edges + 1 {
		fmt.Fprintf(&result, "  service_%02d:\n", level)
		if level < edges {
			fmt.Fprintf(&result, "    extends: service_%02d\n", level+1)
		} else {
			result.WriteString("    image: nginx\n")
		}
	}
	return result.String()
}

func writeProjectCompose(t *testing.T, root, relativePath, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	return writeCompose(t, filepath.Dir(path), filepath.Base(path), contents)
}
