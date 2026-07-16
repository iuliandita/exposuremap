package compose_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	composeparser "github.com/iuliandita/exposuremap/internal/compose"
	"github.com/iuliandita/exposuremap/internal/graph"
)

func parsePrimary(t *testing.T, primary string) graph.Revision {
	t.Helper()
	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{Primary: primary})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	return revision
}

func requireBaseImage(t *testing.T, primary, service, want string) {
	t.Helper()
	revision := parsePrimary(t, primary)
	if got := serviceNamed(t, revision, service).BaseImage; got != want {
		t.Fatalf("BaseImage = %q, want %q", got, want)
	}
}

func requireNoBaseImage(t *testing.T, primary, service string) {
	t.Helper()
	revision := parsePrimary(t, primary)
	if got := serviceNamed(t, revision, service).BaseImage; got != "" {
		t.Fatalf("BaseImage = %q, want empty", got)
	}
}

func TestBuildFactsLongSyntax(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build:
      context: .
      dockerfile: db/Dockerfile.postgres
    image: example/custom-db
`)
	if err := os.MkdirAll(filepath.Join(directory, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCompose(t, filepath.Join(directory, "db"), "Dockerfile.postgres", "FROM postgres:16\n")
	requireBaseImage(t, primary, "db", "postgres:16")
}

func TestBuildFactsShortSyntaxDefaultName(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  cache:
    build: .
`)
	writeCompose(t, directory, "Dockerfile", "FROM redis:7-alpine\n")
	requireBaseImage(t, primary, "cache", "redis:7-alpine")
}

func TestBuildFactsMissingDockerfileStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  app:
    build: .
    image: example/app
`)
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsSymlinkedDockerfileStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  app:
    build: .
`)
	outside := writeCompose(t, t.TempDir(), "Dockerfile", "FROM postgres:16\n")
	if err := os.Symlink(outside, filepath.Join(directory, "Dockerfile")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsContextEscapingRootStaysEmpty(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "deploy")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := writeCompose(t, nested, "compose.yaml", `
services:
  app:
    build:
      context: ../outside
`)
	writeCompose(t, root, "outside-marker", "")
	if err := os.MkdirAll(filepath.Join(root, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCompose(t, filepath.Join(root, "outside"), "Dockerfile", "FROM postgres:16\n")
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsContextInsideRootResolves(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "deploy")
	sibling := filepath.Join(root, "server")
	for _, directory := range []string{nested, sibling} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	primary := writeCompose(t, nested, "compose.yaml", `
services:
  app:
    build:
      context: ../server
      dockerfile: docker/Dockerfile
`)
	if err := os.MkdirAll(filepath.Join(sibling, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCompose(t, filepath.Join(sibling, "docker"), "Dockerfile", "FROM mongo:8\n")
	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Root: root,
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "app").BaseImage; got != "mongo:8" {
		t.Fatalf("BaseImage = %q, want mongo:8", got)
	}
}

func TestBuildFactsRemoteContextStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  app:
    build:
      context: https://github.com/example/repo.git
`)
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsInlineDockerfileStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  app:
    build:
      context: .
      dockerfile_inline: |
        FROM postgres:16
`)
	writeCompose(t, directory, "Dockerfile", "FROM redis:7\n")
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsAdditionalContextsStayEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  app:
    build:
      context: .
      additional_contexts:
        assets: ./assets
`)
	writeCompose(t, directory, "Dockerfile", "FROM postgres:16\n")
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsTargetSelectsStage(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build:
      context: .
      target: datastore
`)
	writeCompose(t, directory, "Dockerfile", `
FROM golang:1.24 AS build
FROM postgres:16 AS datastore
FROM alpine:3.20
`)
	requireBaseImage(t, primary, "db", "postgres:16")
}

func TestBuildFactsUnknownTargetStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build:
      context: .
      target: missing
`)
	writeCompose(t, directory, "Dockerfile", "FROM postgres:16\n")
	requireNoBaseImage(t, primary, "db")
}

func TestBuildFactsBuildArgsResolveFrom(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build:
      context: .
      args:
        BASE: mysql:8
`)
	writeCompose(t, directory, "Dockerfile", "ARG BASE=postgres:16\nFROM ${BASE}\n")
	requireBaseImage(t, primary, "db", "mysql:8")
}

func TestBuildFactsAmbientBuildArgStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build:
      context: .
      args:
        - BASE
`)
	writeCompose(t, directory, "Dockerfile", "FROM ${BASE}\n")
	requireNoBaseImage(t, primary, "db")
}

func TestBuildFactsUnresolvedDockerfileInterpolationStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build:
      context: .
      dockerfile: ${DOCKERFILE_NAME}
`)
	writeCompose(t, directory, "Dockerfile", "FROM postgres:16\n")
	requireNoBaseImage(t, primary, "db")
}

func TestBuildFactsResolvedDockerfileInterpolation(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build:
      context: .
      dockerfile: ${DOCKERFILE_NAME}
`)
	writeCompose(t, directory, "Dockerfile.postgres", "FROM postgres:16\n")
	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary:   primary,
		Variables: map[string]string{"DOCKERFILE_NAME": "Dockerfile.postgres"},
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "db").BaseImage; got != "postgres:16" {
		t.Fatalf("BaseImage = %q, want postgres:16", got)
	}
}

func TestBuildFactsIncludedProjectDockerfile(t *testing.T) {
	root := t.TempDir()
	primary := writeCompose(t, root, "compose.yaml", `
include:
  - path: child/compose.yaml
services:
  web:
    image: nginx
`)
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCompose(t, child, "compose.yaml", `
services:
  db:
    build: .
    image: example/db
`)
	writeCompose(t, child, "Dockerfile", "FROM mariadb:11\n")
	requireBaseImage(t, primary, "db", "mariadb:11")
}

func TestBuildFactsOverrideAddsBuild(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    image: example/db
`)
	override := writeCompose(t, directory, "compose.override.yaml", `
services:
  db:
    build: .
`)
	writeCompose(t, directory, "Dockerfile", "FROM valkey:8\n")
	revision, err := composeparser.ParseProject(context.Background(), composeparser.ProjectInput{
		Primary: primary, Overrides: []string{override},
	})
	if err != nil {
		t.Fatalf("ParseProject() error = %v", err)
	}
	if got := serviceNamed(t, revision, "db").BaseImage; got != "valkey:8" {
		t.Fatalf("BaseImage = %q, want valkey:8", got)
	}
}

func TestBuildFactsOversizedDockerfileStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  app:
    build: .
`)
	content := "FROM postgres:16\n" + strings.Repeat("# padding\n", 200*1000)
	writeCompose(t, directory, "Dockerfile", content)
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsStageReferenceChase(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build: .
`)
	writeCompose(t, directory, "Dockerfile", `
FROM postgres:16 AS base
FROM base
`)
	requireBaseImage(t, primary, "db", "postgres:16")
}

func TestBuildFactsHeredocStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  db:
    build: .
`)
	writeCompose(t, directory, "Dockerfile", "FROM alpine:3.20\nRUN <<EOF\necho hi\nEOF\n")
	requireNoBaseImage(t, primary, "db")
}

func TestBuildFactsAbsoluteDockerfileStaysEmpty(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  app:
    build:
      context: .
      dockerfile: /etc/hostname
`)
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsDockerfileEscapingRootStaysEmpty(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "deploy")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := writeCompose(t, nested, "compose.yaml", `
services:
  app:
    build:
      context: .
      dockerfile: ../Dockerfile
`)
	writeCompose(t, root, "Dockerfile", "FROM postgres:16\n")
	requireNoBaseImage(t, primary, "app")
}

func TestBuildFactsImageOnlyServiceUnchanged(t *testing.T) {
	directory := t.TempDir()
	primary := writeCompose(t, directory, "compose.yaml", `
services:
  web:
    image: nginx
`)
	requireNoBaseImage(t, primary, "web")
}
