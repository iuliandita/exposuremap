package dockerfile

import "testing"

func finalImage(t *testing.T, content string, target string, args map[string]string) (string, bool) {
	t.Helper()
	return Parse([]byte(content)).FinalImage(target, args)
}

func requireImage(t *testing.T, content string, target string, args map[string]string, want string) {
	t.Helper()
	got, ok := finalImage(t, content, target, args)
	if !ok || got != want {
		t.Fatalf("FinalImage() = %q, %v; want %q, true", got, ok, want)
	}
}

func requireNoImage(t *testing.T, content string, target string, args map[string]string) {
	t.Helper()
	if got, ok := finalImage(t, content, target, args); ok {
		t.Fatalf("FinalImage() = %q, true; want no image", got)
	}
}

func TestSingleStage(t *testing.T) {
	requireImage(t, "FROM alpine:3.20\n", "", nil, "alpine:3.20")
}

func TestCaseInsensitiveInstruction(t *testing.T) {
	requireImage(t, "from Alpine:3.20\n", "", nil, "Alpine:3.20")
}

func TestCommentsAndBlankLinesIgnored(t *testing.T) {
	content := "# leading comment\n\n   # indented comment\nFROM redis:7\n# trailing comment\n"
	requireImage(t, content, "", nil, "redis:7")
}

func TestContinuationJoinsLines(t *testing.T) {
	content := "FROM --platform=linux/amd64 \\\n    postgres:16 AS db\n"
	requireImage(t, content, "", nil, "postgres:16")
	requireImage(t, content, "DB", nil, "postgres:16")
}

func TestCommentInsideContinuationRemoved(t *testing.T) {
	content := "FROM \\\n# comment between continuations\npostgres:16\n"
	requireImage(t, content, "", nil, "postgres:16")
}

func TestMultiStageFinalIsLastFrom(t *testing.T) {
	content := "FROM golang:1.24 AS build\nRUN true\nFROM gcr.io/distroless/static\n"
	requireImage(t, content, "", nil, "gcr.io/distroless/static")
}

func TestStageReferenceChasesAlias(t *testing.T) {
	content := "FROM postgres:16 AS base\nFROM base AS final\n"
	requireImage(t, content, "", nil, "postgres:16")
	requireImage(t, content, "base", nil, "postgres:16")
}

func TestStageReferenceIsCaseInsensitive(t *testing.T) {
	content := "FROM mariadb:11 AS DataBase\nFROM database\n"
	requireImage(t, content, "", nil, "mariadb:11")
}

func TestForwardStageReferenceChases(t *testing.T) {
	content := "FROM later\nFROM mongo:8 AS later\n"
	requireImage(t, content, "", nil, "mongo:8")
}

func TestStageCycleHasNoImage(t *testing.T) {
	content := "FROM b AS a\nFROM a AS b\n"
	requireNoImage(t, content, "", nil)
}

func TestTargetSelectsStage(t *testing.T) {
	content := "FROM golang:1.24 AS build\nFROM alpine:3.20\n"
	requireImage(t, content, "build", nil, "golang:1.24")
}

func TestUnknownTargetHasNoImage(t *testing.T) {
	requireNoImage(t, "FROM alpine:3.20\n", "missing", nil)
}

func TestScratchFinalStageHasNoImage(t *testing.T) {
	requireNoImage(t, "FROM scratch\n", "", nil)
}

func TestEmptyAndMissingFrom(t *testing.T) {
	requireNoImage(t, "", "", nil)
	requireNoImage(t, "RUN echo hi\n", "", nil)
}

func TestArgDefaultResolvesFrom(t *testing.T) {
	content := "ARG BASE=postgres:16\nFROM ${BASE}\n"
	requireImage(t, content, "", nil, "postgres:16")
}

func TestExplicitArgOverridesDefault(t *testing.T) {
	content := "ARG BASE=postgres:16\nFROM ${BASE}\n"
	requireImage(t, content, "", map[string]string{"BASE": "mysql:8"}, "mysql:8")
}

func TestUnresolvedArgHasNoImage(t *testing.T) {
	requireNoImage(t, "FROM ${BASE}\n", "", nil)
	requireNoImage(t, "ARG BASE\nFROM ${BASE}\n", "", nil)
}

func TestArgExpansionForms(t *testing.T) {
	requireImage(t, "FROM ${BASE:-postgres:16}\n", "", nil, "postgres:16")
	requireImage(t, "FROM ${BASE:-postgres:16}\n", "", map[string]string{"BASE": ""}, "postgres:16")
	requireNoImage(t, "FROM ${BASE-postgres:16}\n", "", map[string]string{"BASE": ""})
	requireImage(t, "FROM $BASE AS x\n", "", map[string]string{"BASE": "redis:7"}, "redis:7")
	requireImage(t, "FROM registry.local/${BASE}:1\n", "", map[string]string{"BASE": "mongo"}, "registry.local/mongo:1")
	requireImage(t, "FROM ${BASE:+alpine:3.20}\n", "", map[string]string{"BASE": "x"}, "alpine:3.20")
	requireNoImage(t, "FROM ${BASE:+alpine:3.20}\n", "", nil)
}

func TestNestedExpansionUnsupported(t *testing.T) {
	requireNoImage(t, "ARG OUTER=postgres\nFROM ${BASE:-${OUTER}}\n", "", nil)
}

func TestArgDefaultWithDollarUnsupported(t *testing.T) {
	requireNoImage(t, "ARG BASE=$OTHER\nFROM ${BASE}\n", "", nil)
	requireImage(t, "ARG BASE=$OTHER\nFROM ${BASE}\n", "", map[string]string{"BASE": "redis:7"}, "redis:7")
}

func TestArgAfterFromDoesNotApply(t *testing.T) {
	content := "FROM ${BASE:-alpine:3.20}\nARG BASE=postgres:16\n"
	requireImage(t, content, "", nil, "alpine:3.20")
}

func TestArgRedeclareKeepsDefault(t *testing.T) {
	content := "ARG BASE=postgres:16\nARG BASE\nFROM ${BASE}\n"
	requireImage(t, content, "", nil, "postgres:16")
}

func TestNonDefaultEscapeUnsupported(t *testing.T) {
	content := "# escape=`\nFROM alpine:3.20\n"
	requireNoImage(t, content, "", nil)
}

func TestDefaultEscapeDirectiveAccepted(t *testing.T) {
	content := "# syntax=docker/dockerfile:1\n# escape=\\\nFROM alpine:3.20\n"
	requireImage(t, content, "", nil, "alpine:3.20")
}

func TestHeredocUnsupported(t *testing.T) {
	content := "FROM alpine:3.20\nRUN <<EOF\nFROM postgres:16\nEOF\n"
	requireNoImage(t, content, "", nil)
	content = "FROM alpine:3.20\nCOPY <<-EOF /etc/config\nFROM=postgres\nEOF\n"
	requireNoImage(t, content, "", nil)
}

func TestMalformedFromNotExact(t *testing.T) {
	requireNoImage(t, "FROM\n", "", nil)
	requireNoImage(t, "FROM one two three four\n", "", nil)
	requireNoImage(t, "FROM alpine AS base extra\n", "", nil)
	requireNoImage(t, "FROM alpine AS $NAME\n", "", nil)
	requireNoImage(t, "FROM \"postgres:16\"\n", "", nil)
}

func TestCRLFLineEndings(t *testing.T) {
	requireImage(t, "FROM alpine:3.20 \\\r\nAS final\r\n", "", nil, "alpine:3.20")
}
