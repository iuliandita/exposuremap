package evaluate_test

import (
	"bytes"
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
	"github.com/iuliandita/exposuremap/internal/report"
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

func TestRunCasePreservesPeerChangesWithoutScorerFinding(t *testing.T) {
	item := evaluationCase([]string{"compose.yaml"})
	item.Kind = benchmark.CaseReal
	item.ExpectedBoundary = ""
	cache := materializeEvaluationCase(t, item, map[string][]byte{
		item.BaseSHA: []byte(evaluationPeerCompose(false)),
		item.HeadSHA: []byte(evaluationPeerCompose(true)),
	})

	outcome, err := evaluate.RunCase(context.Background(), cache, item)
	if err != nil {
		t.Fatalf("RunCase() error = %v", err)
	}
	if got := outcome.Report.Analyzer.Changes; len(got) != 1 || got[0].Kind != report.ChangePeerPathAdded {
		t.Fatalf("analyzer changes = %#v", got)
	}
	if len(outcome.Result.Findings) != 0 || outcome.Result.DetectedBoundary || outcome.Result.ProposedFailure {
		t.Fatalf("scorer result absorbed informational changes: %#v", outcome.Result)
	}
	if outcome.Result.RulePackVersion != "compose-v3" || outcome.Report.Analyzer.SchemaVersion != 2 {
		t.Fatalf("version contract = %#v", outcome)
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
	if outcome.Result.Findings == nil || outcome.Report.Analyzer.Changes == nil || outcome.Report.Analyzer.Findings == nil {
		t.Fatalf("incomplete collections are nil: %#v", outcome)
	}
	if outcome.Report.Analyzer.Diagnostics[0] != "base: parse compose file: unresolved boundary interpolation in services.web.image" ||
		outcome.Report.Analyzer.Diagnostics[1] != "head: parse compose file: unresolved boundary interpolation in services.web.image" {
		t.Fatalf("diagnostics = %#v", outcome.Report.Analyzer.Diagnostics)
	}
}

func TestBuildAndWriteBundleProducesDeterministicReviewArtifacts(t *testing.T) {
	item := evaluationCase([]string{"compose.yaml"})
	item.Kind = benchmark.CaseReal
	item.ExpectedBoundary = ""
	cache := materializeEvaluationCase(t, item, map[string][]byte{
		item.BaseSHA: []byte(evaluationPeerCompose(false)),
		item.HeadSHA: []byte(evaluationPeerCompose(true)),
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

	for _, relative := range []string{
		"results.json",
		"summary.json",
		filepath.Join("reports", item.ID+".json"),
		filepath.Join("reports", item.ID+".md"),
	} {
		first := mustReadFile(t, filepath.Join(firstOutput, relative))
		second := mustReadFile(t, filepath.Join(secondOutput, relative))
		if !bytes.Equal(first, second) {
			t.Fatalf("%s differs between runs", relative)
		}
		info, err := os.Stat(filepath.Join(firstOutput, relative))
		if err != nil {
			t.Fatalf("stat %s: %v", relative, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode(%s) = %v", relative, info.Mode())
		}
	}
	var results []review.Result
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(firstOutput, "results.json")), &results); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if len(results) != 1 || results[0].CaseID != item.ID || len(results[0].Findings) != 0 {
		t.Fatalf("results = %#v", results)
	}
	if bundle.Summary.Changes != 1 || bundle.Summary.ChangeKinds["peer-path-added"] != 1 {
		t.Fatalf("summary = %#v", bundle.Summary)
	}
	if bundle.Summary.Findings != 0 || len(bundle.Summary.Rules) != 0 {
		t.Fatalf("summary = %#v", bundle.Summary)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	return content
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

func evaluationPeerCompose(includeDashboard bool) string {
	contents := "services:\n" +
		"  gateway:\n" +
		"    image: example/gateway:1\n" +
		"    ports: ['127.0.0.1:8080:80']\n" +
		"    networks: [inbound]\n"
	if includeDashboard {
		contents += "  dashboard:\n" +
			"    image: grafana/grafana:latest\n" +
			"    networks: [inbound]\n"
	}
	return contents + "networks:\n  inbound: {}\n"
}
