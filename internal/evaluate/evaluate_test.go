package evaluate_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iuliandita/exposuremap/internal/benchmark"
	"github.com/iuliandita/exposuremap/internal/evaluate"
	"github.com/iuliandita/exposuremap/internal/githubapi"
	"github.com/iuliandita/exposuremap/internal/review"
)

func TestRunCaseProducesScorerResultAndStableFindingID(t *testing.T) {
	item := evaluationCase([]string{"compose.yaml"})
	cache := materializeEvaluationCase(t, item, map[string][]byte{
		item.BaseSHA: []byte("services:\n  web:\n    image: example/web:1\n"),
		item.HeadSHA: []byte("services:\n  web:\n    image: example/web:1\n    ports:\n      - '127.0.0.1:8080:80'\n"),
	})

	first, err := evaluate.RunCase(context.Background(), cache, item)
	if err != nil {
		t.Fatalf("RunCase() error = %v", err)
	}
	second, err := evaluate.RunCase(context.Background(), cache, item)
	if err != nil {
		t.Fatalf("second RunCase() error = %v", err)
	}

	if first.Result.Status != review.StatusComplete || first.Result.ObservedElements != 2 || first.Result.ResolvedElements != 2 {
		t.Fatalf("result = %#v", first.Result)
	}
	if len(first.Result.Findings) != 1 || first.Result.Findings[0].RuleID != "EM-COMPOSE-001" || first.Result.Findings[0].ID == "" {
		t.Fatalf("findings = %#v", first.Result.Findings)
	}
	if first.Result.Findings[0].ID != second.Result.Findings[0].ID {
		t.Fatalf("finding IDs differ: %q != %q", first.Result.Findings[0].ID, second.Result.Findings[0].ID)
	}
	if !first.Result.DetectedBoundary || first.Report.Analyzer.Findings[0].RuleID != "EM-COMPOSE-001" {
		t.Fatalf("outcome = %#v", first)
	}
}

func TestRunCaseMarksUnsupportedComposePathCountIncomplete(t *testing.T) {
	for name, paths := range map[string][]string{
		"none":     {"Dockerfile"},
		"multiple": {"compose.yaml", "deploy/compose.yml"},
	} {
		t.Run(name, func(t *testing.T) {
			item := evaluationCase(paths)
			content := map[string][]byte{
				item.BaseSHA: []byte("services: {}\n"),
				item.HeadSHA: []byte("services: {}\n"),
			}
			cache := materializeEvaluationCase(t, item, content)

			outcome, err := evaluate.RunCase(context.Background(), cache, item)
			if err != nil {
				t.Fatalf("RunCase() error = %v", err)
			}
			if outcome.Result.Status != review.StatusIncomplete || len(outcome.Report.Analyzer.Diagnostics) != 1 {
				t.Fatalf("outcome = %#v", outcome)
			}
			if !strings.Contains(outcome.Report.Analyzer.Diagnostics[0], "Compose paths") {
				t.Fatalf("diagnostics = %#v", outcome.Report.Analyzer.Diagnostics)
			}
		})
	}
}

func TestRunCaseReportsBothIncompleteRevisions(t *testing.T) {
	item := evaluationCase([]string{"compose.yaml"})
	cache := materializeEvaluationCase(t, item, map[string][]byte{
		item.BaseSHA: []byte("services:\n  web:\n    image: '${BASE_IMAGE:?required}'\n"),
		item.HeadSHA: []byte("services:\n  web:\n    image: '${HEAD_IMAGE:?required}'\n"),
	})

	outcome, err := evaluate.RunCase(context.Background(), cache, item)
	if err != nil {
		t.Fatalf("RunCase() error = %v", err)
	}
	if outcome.Result.Status != review.StatusIncomplete || len(outcome.Report.Analyzer.Diagnostics) != 2 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if outcome.Result.Findings == nil {
		t.Fatal("incomplete result findings = nil, want stable empty array")
	}
	if outcome.Report.Analyzer.Diagnostics[0] != "base: parse compose file: invalid or unresolved Compose model" ||
		outcome.Report.Analyzer.Diagnostics[1] != "head: parse compose file: invalid or unresolved Compose model" {
		t.Fatalf("diagnostics = %#v", outcome.Report.Analyzer.Diagnostics)
	}
}

func TestBuildAndWriteBundleProducesDeterministicReviewArtifacts(t *testing.T) {
	item := evaluationCase([]string{"compose.yaml"})
	cache := materializeEvaluationCase(t, item, map[string][]byte{
		item.BaseSHA: []byte("services:\n  web:\n    image: example/web:1\n"),
		item.HeadSHA: []byte("services:\n  web:\n    image: example/web:1\n    ports:\n      - '127.0.0.1:8080:80'\n"),
	})
	corpus := benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}}

	bundle, err := evaluate.Build(context.Background(), cache, corpus)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	firstOutput := filepath.Join(t.TempDir(), "run")
	if err := evaluate.WriteBundle(firstOutput, bundle); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	secondOutput := filepath.Join(t.TempDir(), "run")
	if err := evaluate.WriteBundle(secondOutput, bundle); err != nil {
		t.Fatalf("second WriteBundle() error = %v", err)
	}

	firstResults, err := os.ReadFile(filepath.Join(firstOutput, "results.json"))
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	secondResults, err := os.ReadFile(filepath.Join(secondOutput, "results.json"))
	if err != nil {
		t.Fatalf("read second results: %v", err)
	}
	if string(firstResults) != string(secondResults) {
		t.Fatalf("results differ:\n%s\n%s", firstResults, secondResults)
	}
	var results []review.Result
	if err := json.Unmarshal(firstResults, &results); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if len(results) != 1 || results[0].CaseID != item.ID {
		t.Fatalf("results = %#v", results)
	}
	for _, relative := range []string{
		"summary.json",
		filepath.Join("reports", item.ID+".json"),
		filepath.Join("reports", item.ID+".md"),
	} {
		info, err := os.Stat(filepath.Join(firstOutput, relative))
		if err != nil {
			t.Fatalf("stat %s: %v", relative, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode(%s) = %v", relative, info.Mode())
		}
	}
	if bundle.Summary.Cases != 1 || bundle.Summary.Findings != 1 || bundle.Summary.Rules["EM-COMPOSE-001"] != 1 {
		t.Fatalf("summary = %#v", bundle.Summary)
	}
}

type evaluationContentClient struct {
	files map[string][]byte
}

func (client *evaluationContentClient) ListFiles(
	_ context.Context,
	_, _ string,
	sha string,
	paths []string,
) (map[string]githubapi.FileMetadata, error) {
	result := make(map[string]githubapi.FileMetadata, len(paths))
	for _, path := range paths {
		if _, exists := client.files[sha+"|"+path]; !exists {
			return nil, errors.New("missing test content")
		}
		result[path] = githubapi.FileMetadata{BlobSHA: "blob-" + sha[:1] + "-" + strings.ReplaceAll(path, "/", "-")}
	}
	return result, nil
}

func (client *evaluationContentClient) GetFile(
	_ context.Context,
	_, _ string,
	sha string,
	path string,
	_ int,
) (githubapi.File, error) {
	content, exists := client.files[sha+"|"+path]
	if !exists {
		return githubapi.File{}, errors.New("missing test content")
	}
	return githubapi.File{
		BlobSHA: "blob-" + sha[:1] + "-" + strings.ReplaceAll(path, "/", "-"),
		Content: content,
	}, nil
}

func evaluationCase(paths []string) benchmark.Case {
	return benchmark.Case{
		ID:               "acme-widget-pr-7",
		Repository:       "https://github.com/acme/widget",
		LicenseURL:       "https://github.com/acme/widget/blob/main/LICENSE",
		LicenseSPDX:      "MIT",
		PullRequest:      7,
		MergedAt:         time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		BaseSHA:          strings.Repeat("a", 40),
		HeadSHA:          strings.Repeat("b", 40),
		Kind:             benchmark.CaseMutation,
		Split:            benchmark.SplitTraining,
		Paths:            paths,
		ExpectedBoundary: "EM-COMPOSE-001",
	}
}

func materializeEvaluationCase(t *testing.T, item benchmark.Case, contentBySHA map[string][]byte) string {
	t.Helper()
	files := make(map[string][]byte, len(item.Paths)*2)
	for _, sha := range []string{item.BaseSHA, item.HeadSHA} {
		for _, path := range item.Paths {
			files[sha+"|"+path] = contentBySHA[sha]
		}
	}
	cache := filepath.Join(t.TempDir(), "cases")
	_, err := benchmark.Materialize(
		context.Background(),
		&evaluationContentClient{files: files},
		benchmark.Corpus{SchemaVersion: 1, Cases: []benchmark.Case{item}},
		cache,
	)
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	return cache
}
