package benchmark_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iuliandita/exposuremap/internal/benchmark"
	"github.com/iuliandita/exposuremap/internal/githubapi"
)

func TestMaterializeWritesChecksummedImmutableCase(t *testing.T) {
	corpus := materializeCorpus([]string{"deploy/compose.yaml", "Dockerfile"})
	client := &fakeContentClient{files: map[string]githubapi.File{}}
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		for _, path := range corpus.Cases[0].Paths {
			client.files[sha+"|"+path] = githubapi.File{
				BlobSHA: "blob-" + path,
				Content: []byte(sha[:1] + ":" + path),
			}
		}
	}
	output := filepath.Join(t.TempDir(), "cache")

	summary, err := benchmark.Materialize(context.Background(), client, corpus, output)
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	if summary.Cases != 1 || summary.Files != 4 || summary.Bytes == 0 || summary.Output != output {
		t.Fatalf("summary = %#v", summary)
	}
	wantCalls := []string{
		strings.Repeat("a", 40) + "|Dockerfile",
		strings.Repeat("a", 40) + "|deploy/compose.yaml",
		strings.Repeat("b", 40) + "|Dockerfile",
		strings.Repeat("b", 40) + "|deploy/compose.yaml",
	}
	if !slices.Equal(client.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", client.calls, wantCalls)
	}

	caseDirectory := filepath.Join(output, corpus.Cases[0].ID)
	manifest, err := benchmark.ReadMaterializedManifest(filepath.Join(caseDirectory, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadMaterializedManifest() error = %v", err)
	}
	if manifest.Repository != corpus.Cases[0].Repository || manifest.LicenseSPDX != "MIT" {
		t.Fatalf("manifest metadata = %#v", manifest)
	}
	if len(manifest.Base.Files) != 2 || manifest.Base.Files[0].Path != "Dockerfile" || manifest.Base.Files[0].SHA256 == "" {
		t.Fatalf("base manifest = %#v", manifest.Base)
	}
	for _, path := range []string{
		filepath.Join(caseDirectory, "base", "Dockerfile"),
		filepath.Join(caseDirectory, "head", "deploy", "compose.yaml"),
		filepath.Join(caseDirectory, "manifest.json"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Lstat(%s) error = %v", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Errorf("mode(%s) = %v, want regular 0600", path, info.Mode())
		}
	}
}

func TestMaterializeReusesOnlyVerifiedCache(t *testing.T) {
	corpus := materializeCorpus([]string{"compose.yaml"})
	client := &fakeContentClient{files: map[string]githubapi.File{}}
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		client.files[sha+"|compose.yaml"] = githubapi.File{BlobSHA: "blob", Content: []byte(sha)}
	}
	output := filepath.Join(t.TempDir(), "cache")
	if _, err := benchmark.Materialize(context.Background(), client, corpus, output); err != nil {
		t.Fatalf("first Materialize() error = %v", err)
	}

	reuseClient := &fakeContentClient{err: errors.New("network must not be used")}
	if _, err := benchmark.Materialize(context.Background(), reuseClient, corpus, output); err != nil {
		t.Fatalf("reuse Materialize() error = %v", err)
	}
	if len(reuseClient.calls) != 0 {
		t.Fatalf("reuse calls = %#v, want none", reuseClient.calls)
	}

	baseFile := filepath.Join(output, corpus.Cases[0].ID, "base", "compose.yaml")
	if err := os.WriteFile(baseFile, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper WriteFile() error = %v", err)
	}
	if _, err := benchmark.Materialize(context.Background(), reuseClient, corpus, output); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered Materialize() error = %v, want checksum failure", err)
	}
}

func TestVerifyMaterializedAcceptsValidCacheAndRejectsTampering(t *testing.T) {
	corpus := materializeCorpus([]string{"compose.yaml"})
	client := &fakeContentClient{files: map[string]githubapi.File{}}
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		client.files[sha+"|compose.yaml"] = githubapi.File{
			BlobSHA: "blob-" + sha[:1],
			Content: []byte("services: {}\n# " + sha),
		}
	}
	output := filepath.Join(t.TempDir(), "cache")
	if _, err := benchmark.Materialize(context.Background(), client, corpus, output); err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}

	manifest, err := benchmark.VerifyMaterialized(output, corpus.Cases[0])
	if err != nil {
		t.Fatalf("VerifyMaterialized() error = %v", err)
	}
	if manifest.CaseID != corpus.Cases[0].ID || manifest.Base.Files[0].BlobSHA != "blob-a" {
		t.Fatalf("manifest = %#v", manifest)
	}

	basePath := filepath.Join(output, corpus.Cases[0].ID, "base", "compose.yaml")
	if err := os.WriteFile(basePath, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper cache: %v", err)
	}
	if _, err := benchmark.VerifyMaterialized(output, corpus.Cases[0]); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("VerifyMaterialized() error = %v, want checksum failure", err)
	}
}

func TestVerifyMaterializedRejectsUnlistedFiles(t *testing.T) {
	corpus := materializeCorpus([]string{"compose.yaml"})
	client := &fakeContentClient{files: map[string]githubapi.File{}}
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		client.files[sha+"|compose.yaml"] = githubapi.File{
			BlobSHA: "blob-" + sha[:1],
			Content: []byte("include:\n  - unlisted.yaml\n# " + sha),
		}
	}
	output := filepath.Join(t.TempDir(), "cache")
	if _, err := benchmark.Materialize(context.Background(), client, corpus, output); err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	extra := filepath.Join(output, corpus.Cases[0].ID, "base", "unlisted.yaml")
	if err := os.WriteFile(extra, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := benchmark.VerifyMaterialized(output, corpus.Cases[0]); err == nil ||
		!strings.Contains(err.Error(), "unexpected cache entry") {
		t.Fatalf("VerifyMaterialized() error = %v, want unexpected-entry failure", err)
	}
}

func TestMaterializeRejectsUnsafeAndExcessiveCasesBeforeFetching(t *testing.T) {
	tests := map[string]benchmark.Corpus{
		"unsafe case id": materializeCorpusWithID("../escape", []string{"compose.yaml"}),
		"unsafe path":    materializeCorpus([]string{"../compose.yaml"}),
		"too many paths": materializeCorpus(numberedPaths(65)),
	}
	for name, corpus := range tests {
		t.Run(name, func(t *testing.T) {
			client := &fakeContentClient{}
			_, err := benchmark.Materialize(context.Background(), client, corpus, filepath.Join(t.TempDir(), "cache"))
			if err == nil {
				t.Fatal("Materialize() error = nil")
			}
			if len(client.calls) != 0 {
				t.Fatalf("calls = %#v, want none", client.calls)
			}
		})
	}
}

func TestMaterializeLeavesNoCaseAfterFetchOrSizeFailure(t *testing.T) {
	corpus := materializeCorpus([]string{"compose.yaml"})
	for name, client := range map[string]*fakeContentClient{
		"fetch": {err: errors.New("unavailable")},
		"total size": {files: map[string]githubapi.File{
			strings.Repeat("a", 40) + "|compose.yaml": {BlobSHA: "a", Content: make([]byte, (4<<20)+1)},
			strings.Repeat("b", 40) + "|compose.yaml": {BlobSHA: "b", Content: make([]byte, (4<<20)+1)},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "cache")
			_, err := benchmark.Materialize(context.Background(), client, corpus, output)
			if err == nil {
				t.Fatal("Materialize() error = nil")
			}
			if _, statError := os.Stat(filepath.Join(output, corpus.Cases[0].ID)); !errors.Is(statError, os.ErrNotExist) {
				t.Fatalf("case directory exists after failure: %v", statError)
			}
		})
	}
}

func TestMaterializeRejectsTotalCaseSize(t *testing.T) {
	corpus := materializeCorpus(numberedPaths(5))
	client := &fakeContentClient{files: map[string]githubapi.File{}}
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		for _, path := range corpus.Cases[0].Paths {
			client.files[sha+"|"+path] = githubapi.File{
				BlobSHA: "blob",
				Content: make([]byte, 900<<10),
			}
		}
	}
	output := filepath.Join(t.TempDir(), "cache")

	_, err := benchmark.Materialize(context.Background(), client, corpus, output)

	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Materialize() error = %v, want total-size rejection", err)
	}
	if _, statError := os.Stat(filepath.Join(output, corpus.Cases[0].ID)); !errors.Is(statError, os.ErrNotExist) {
		t.Fatalf("case directory exists after size failure: %v", statError)
	}
}

func TestMaterializeRejectsRepositorySymlinkBeforeFetchingContent(t *testing.T) {
	corpus := materializeCorpus([]string{"compose.yaml"})
	client := &fakeContentClient{
		files: map[string]githubapi.File{
			strings.Repeat("a", 40) + "|compose.yaml": {BlobSHA: "target", Content: []byte("services: {}\n")},
			strings.Repeat("b", 40) + "|compose.yaml": {BlobSHA: "target", Content: []byte("services: {}\n")},
		},
		listErr: errors.New("repository path is not a regular file"),
	}

	_, err := benchmark.Materialize(context.Background(), client, corpus, filepath.Join(t.TempDir(), "cache"))

	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Materialize() error = %v, want symlink rejection", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("content calls = %#v, want none", client.calls)
	}
}

type fakeContentClient struct {
	files     map[string]githubapi.File
	err       error
	listErr   error
	calls     []string
	listCalls []string
}

func (client *fakeContentClient) ListFiles(
	_ context.Context,
	_, _ string,
	sha string,
	paths []string,
) (map[string]githubapi.FileMetadata, error) {
	client.listCalls = append(client.listCalls, sha)
	if client.listErr != nil {
		return nil, client.listErr
	}
	result := make(map[string]githubapi.FileMetadata, len(paths))
	for _, path := range paths {
		file, exists := client.files[sha+"|"+path]
		if !exists {
			return nil, errors.New("missing fake metadata")
		}
		result[path] = githubapi.FileMetadata{BlobSHA: file.BlobSHA}
	}
	return result, nil
}

func (client *fakeContentClient) GetFile(
	_ context.Context,
	_, _ string,
	sha string,
	path string,
	_ int,
) (githubapi.File, error) {
	client.calls = append(client.calls, sha+"|"+path)
	if client.err != nil {
		return githubapi.File{}, client.err
	}
	file, exists := client.files[sha+"|"+path]
	if !exists {
		return githubapi.File{}, errors.New("missing fake file")
	}
	return file, nil
}

func materializeCorpus(paths []string) benchmark.Corpus {
	return materializeCorpusWithID("acme-widget-pr-7", paths)
}

func materializeCorpusWithID(id string, paths []string) benchmark.Corpus {
	return benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{{
		ID:          id,
		Repository:  "https://github.com/acme/widget",
		LicenseURL:  "https://github.com/acme/widget/blob/main/LICENSE",
		LicenseSPDX: "MIT",
		PullRequest: 7,
		MergedAt:    time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		BaseSHA:     strings.Repeat("a", 40),
		HeadSHA:     strings.Repeat("b", 40),
		Kind:        benchmark.CaseReal,
		Split:       benchmark.SplitTraining,
		Paths:       paths,
	}}}
}

func numberedPaths(count int) []string {
	result := make([]string, 0, count)
	for index := range count {
		result = append(result, "configs/compose."+strings.Repeat("x", index+1)+".yaml")
	}
	return result
}
