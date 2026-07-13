package mutate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iuliandita/exposuremap/internal/benchmark"
	"github.com/iuliandita/exposuremap/internal/evaluate"
	"github.com/iuliandita/exposuremap/internal/githubapi"
	"github.com/iuliandita/exposuremap/internal/mutate"
	"github.com/iuliandita/exposuremap/internal/review"
)

func TestGenerateCreatesOneIsolatedCompleteMutationPerRule(t *testing.T) {
	item := mutationSourceCase()
	cache := materializeMutationSource(t, item, []byte(`services:
  app:
    image: example/app:1
`))
	corpus := benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}}

	bundle, err := mutate.Generate(context.Background(), cache, corpus, 1)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(bundle.Corpus.Cases) != 4 || len(bundle.Cases) != 4 {
		t.Fatalf("bundle sizes = corpus:%d cases:%d", len(bundle.Corpus.Cases), len(bundle.Cases))
	}
	output := filepath.Join(t.TempDir(), "mutations")
	if err := mutate.WriteBundle(output, bundle); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}

	mutationCorpus := bundle.Corpus
	evaluation, err := evaluate.Build(context.Background(), filepath.Join(output, "cases"), mutationCorpus)
	if err != nil {
		t.Fatalf("evaluate.Build() error = %v", err)
	}
	if evaluation.Summary.Complete != 4 || evaluation.Summary.Incomplete != 0 {
		t.Fatalf("evaluation summary = %#v", evaluation.Summary)
	}
	for index, result := range evaluation.Results {
		if result.Status != review.StatusComplete || !result.DetectedBoundary || len(result.Findings) != 1 {
			t.Fatalf("result[%d] = %#v", index, result)
		}
		if result.Findings[0].RuleID != mutationCorpus.Cases[index].ExpectedBoundary {
			t.Fatalf("result[%d] rule = %q, want %q", index, result.Findings[0].RuleID, mutationCorpus.Cases[index].ExpectedBoundary)
		}
		if mutationCorpus.Cases[index].ExpectedBoundary == "EM-COMPOSE-001" {
			finding := evaluation.Reports[index].Analyzer.Findings[0]
			if finding.Evidence != "*:41001->8080/tcp" {
				t.Fatalf("new-port evidence = %q, want wildcard host exposure", finding.Evidence)
			}
		}
	}
	if info, err := os.Stat(filepath.Join(output, "corpus.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("corpus mode/info = %v, %v", info, err)
	}
}

func TestGenerateRejectsExcessiveMutationCount(t *testing.T) {
	_, err := mutate.Generate(context.Background(), "unused", benchmark.Corpus{}, 101)
	if err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("Generate() error = %v, want bounded-count rejection", err)
	}
}

func TestGenerateAvoidsReservedServiceNameCollisions(t *testing.T) {
	item := mutationSourceCase()
	cache := materializeMutationSource(t, item, []byte(`services:
  exposuremap_mutation_001_01:
    image: example/existing:1
`))
	corpus := benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}}

	bundle, err := mutate.Generate(context.Background(), cache, corpus, 1)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(bundle.Cases[0].Head), "exposuremap_mutation_001_01_2:") {
		t.Fatalf("head mutation did not use a collision-free service name:\n%s", bundle.Cases[0].Head)
	}
}

type mutationContentClient struct {
	content []byte
}

func (client *mutationContentClient) ListFiles(
	_ context.Context,
	_, _ string,
	sha string,
	paths []string,
) (map[string]githubapi.FileMetadata, error) {
	if len(paths) != 1 {
		return nil, errors.New("unexpected paths")
	}
	return map[string]githubapi.FileMetadata{
		paths[0]: {BlobSHA: "blob-" + sha[:1]},
	}, nil
}

func (client *mutationContentClient) GetFile(
	_ context.Context,
	_, _ string,
	sha string,
	_ string,
	_ int,
) (githubapi.File, error) {
	return githubapi.File{BlobSHA: "blob-" + sha[:1], Content: client.content}, nil
}

func mutationSourceCase() benchmark.Case {
	return benchmark.Case{
		ID:          "acme-widget-pr-7",
		Repository:  "https://github.com/acme/widget",
		LicenseURL:  "https://github.com/acme/widget/blob/main/LICENSE",
		LicenseSPDX: "MIT",
		PullRequest: 7,
		MergedAt:    time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		BaseSHA:     strings.Repeat("a", 40),
		HeadSHA:     strings.Repeat("b", 40),
		Kind:        benchmark.CaseReal,
		Split:       benchmark.SplitTraining,
		Paths:       []string{"compose.yaml"},
	}
}

func materializeMutationSource(t *testing.T, item benchmark.Case, content []byte) string {
	t.Helper()
	cache := filepath.Join(t.TempDir(), "cases")
	_, err := benchmark.Materialize(
		context.Background(),
		&mutationContentClient{content: content},
		benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}},
		cache,
	)
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	return cache
}
