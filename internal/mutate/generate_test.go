package mutate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

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
  db:
    image: postgres:16-alpine
`))
	corpus := benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}}

	bundle, err := mutate.Generate(context.Background(), cache, corpus, 1)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(bundle.Corpus.Cases) != 5 || len(bundle.Cases) != 5 {
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
	if evaluation.Summary.Complete != 5 || evaluation.Summary.Incomplete != 0 {
		t.Fatalf("evaluation summary = %#v", evaluation.Summary)
	}
	for index, result := range evaluation.Results {
		if result.Status != review.StatusComplete || !result.DetectedBoundary || len(result.Findings) != 1 {
			t.Fatalf("result[%d] = %#v", index, result)
		}
		if result.Findings[0].RuleID != mutationCorpus.Cases[index].ExpectedBoundary {
			t.Fatalf("result[%d] rule = %q, want %q", index, result.Findings[0].RuleID, mutationCorpus.Cases[index].ExpectedBoundary)
		}
		if result.Findings[0].Level != review.LevelWarning {
			t.Fatalf("result[%d] finding level = %q, want warning", index, result.Findings[0].Level)
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

func TestGenerateCreatesTwentyValidCasesPerRule(t *testing.T) {
	item := mutationSourceCase()
	cache := materializeMutationSource(t, item, []byte(`services:
  app:
    image: example/app:1
`))
	corpus := benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}}

	bundle, err := mutate.Generate(context.Background(), cache, corpus, 20)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(bundle.Corpus.Cases) != 100 || len(bundle.Cases) != 100 {
		t.Fatalf("bundle sizes = corpus:%d cases:%d, want 100 each", len(bundle.Corpus.Cases), len(bundle.Cases))
	}
	counts := make(map[string]int)
	for _, item := range bundle.Corpus.Cases {
		counts[item.ExpectedBoundary]++
	}
	for _, ruleID := range []string{
		"EM-COMPOSE-001",
		"EM-COMPOSE-002",
		"EM-COMPOSE-003",
		"EM-COMPOSE-004",
		"EM-COMPOSE-005",
	} {
		if counts[ruleID] != 20 {
			t.Fatalf("%s count = %d, want 20; all counts = %#v", ruleID, counts[ruleID], counts)
		}
	}
}

func TestGenerateDataServiceMutationAddsIsolatedSourceAndTarget(t *testing.T) {
	item := mutationSourceCase()
	cache := materializeMutationSource(t, item, []byte(`services:
  app:
    image: example/app:1
`))
	bundle, err := mutate.Generate(
		context.Background(),
		cache,
		benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}},
		1,
	)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var generated mutate.Case
	for _, candidate := range bundle.Cases {
		if candidate.Item.ExpectedBoundary == "EM-COMPOSE-005" {
			generated = candidate
			break
		}
	}
	if generated.Item.ID == "" {
		t.Fatal("rule 005 mutation not found")
	}

	base := decodeMutationCompose(t, generated.Base)
	head := decodeMutationCompose(t, generated.Head)
	if len(base.Services) != 3 || len(head.Services) != 3 {
		t.Fatalf("service counts = base:%d head:%d, want three", len(base.Services), len(head.Services))
	}
	if !reflect.DeepEqual(base.Services["app"], head.Services["app"]) {
		t.Fatalf("original app changed: base=%#v head=%#v", base.Services["app"], head.Services["app"])
	}

	const sourceName = "exposuremap_mutation_005_01"
	const targetName = sourceName + "_target"
	const networkName = sourceName + "_network"
	if len(base.Networks) != 1 || len(head.Networks) != 1 || base.Networks[networkName] == nil || head.Networks[networkName] == nil {
		t.Fatalf("networks = base:%#v head:%#v, want dedicated %q", base.Networks, head.Networks, networkName)
	}
	baseSource := base.Services[sourceName]
	headSource := head.Services[sourceName]
	if baseSource.Image != "scratch" || headSource.Image != "scratch" ||
		!reflect.DeepEqual(baseSource.Ports, []string{"127.0.0.1:41001:8080"}) ||
		!reflect.DeepEqual(baseSource.Ports, headSource.Ports) {
		t.Fatalf("source services = base:%#v head:%#v", baseSource, headSource)
	}
	if baseSource.NetworkMode != "none" || headSource.NetworkMode != "" ||
		len(baseSource.Networks) != 0 || !reflect.DeepEqual(headSource.Networks, []string{networkName}) {
		t.Fatalf("source networking = base:%#v head:%#v", baseSource, headSource)
	}
	baseTarget := base.Services[targetName]
	headTarget := head.Services[targetName]
	if baseTarget.Image != "postgres:16-alpine" || !reflect.DeepEqual(baseTarget, headTarget) ||
		baseTarget.NetworkMode != "" || len(baseTarget.Ports) != 0 ||
		!reflect.DeepEqual(baseTarget.Networks, []string{networkName}) {
		t.Fatalf("target services = base:%#v head:%#v", baseTarget, headTarget)
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
  exposuremap_mutation_005_01:
    image: example/existing:1
  exposuremap_mutation_005_01_target:
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
	var dataServiceMutation mutate.Case
	for _, candidate := range bundle.Cases {
		if candidate.Item.ExpectedBoundary == "EM-COMPOSE-005" {
			dataServiceMutation = candidate
			break
		}
	}
	if !strings.Contains(string(dataServiceMutation.Head), "exposuremap_mutation_005_01_2:") ||
		!strings.Contains(string(dataServiceMutation.Head), "exposuremap_mutation_005_01_target_2:") {
		t.Fatalf("rule 005 mutation did not use collision-free names:\n%s", dataServiceMutation.Head)
	}
}

type mutationCompose struct {
	Services map[string]mutationService `yaml:"services"`
	Networks map[string]map[string]any  `yaml:"networks"`
}

type mutationService struct {
	Image       string   `yaml:"image"`
	NetworkMode string   `yaml:"network_mode"`
	Networks    []string `yaml:"networks"`
	Ports       []string `yaml:"ports"`
}

func decodeMutationCompose(t *testing.T, content []byte) mutationCompose {
	t.Helper()
	var document mutationCompose
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	return document
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
