package cli_test

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
	"github.com/iuliandita/exposuremap/internal/cli"
	"github.com/iuliandita/exposuremap/internal/githubapi"
	"github.com/iuliandita/exposuremap/internal/review"
)

func TestBenchmarkValidateCommand(t *testing.T) {
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	writeJSON(t, corpusPath, oneCaseCorpus())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Run([]string{"benchmark", "validate", "--corpus", corpusPath}, &stdout, &stderr)

	if code != 0 || !strings.Contains(stdout.String(), `"valid": true`) || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestBenchmarkValidateCommandReturnsOneForInvalidCorpus(t *testing.T) {
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	corpus := oneCaseCorpus()
	corpus.SchemaVersion = 2
	writeJSON(t, corpusPath, corpus)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Run([]string{"benchmark", "validate", "--corpus", corpusPath}, &stdout, &stderr)

	if code != 1 || !strings.Contains(stdout.String(), `"valid": false`) || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestBenchmarkMineCommandWritesPrivateCorpus(t *testing.T) {
	directory := t.TempDir()
	seedsPath := filepath.Join(directory, "seeds.json")
	outputPath := filepath.Join(directory, "corpus.json")
	writeJSON(t, seedsPath, benchmark.SeedManifest{
		SchemaVersion: 1,
		ObservedAt:    "2026-07-12",
		Repositories: []benchmark.Seed{{
			Repository:     "https://github.com/acme/widget",
			DefaultBranch:  "main",
			CandidatePaths: []string{"compose.yaml"},
			LicenseURL:     "https://github.com/acme/widget/blob/main/LICENSE",
			LicenseSPDX:    "MIT",
		}},
	})
	client := &cliHistoryClient{}
	var receivedToken string
	dependencies := cli.Dependencies{
		Getenv: func(name string) string {
			if name == "GITHUB_TOKEN" {
				return "test-token"
			}
			return ""
		},
		NewHistoryClient: func(token string) benchmark.HistoryClient {
			receivedToken = token
			return client
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.RunWithDependencies([]string{
		"benchmark", "mine",
		"--seeds", seedsPath,
		"--out", outputPath,
		"--split", "training",
		"--commits-per-path", "1",
		"--limit", "1",
	}, &stdout, &stderr, dependencies)

	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if receivedToken != "test-token" || strings.Contains(stdout.String(), receivedToken) {
		t.Fatalf("token handling failed: received=%q stdout=%q", receivedToken, stdout.String())
	}
	var corpus benchmark.Corpus
	readJSON(t, outputPath, &corpus)
	if len(corpus.Cases) != 1 || corpus.Cases[0].PullRequest != 7 {
		t.Fatalf("mined corpus = %#v", corpus)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("output permissions = %o, want 600", permissions)
	}
}

func TestBenchmarkScoreCommand(t *testing.T) {
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	resultsPath := filepath.Join(directory, "results.json")
	reviewsPath := filepath.Join(directory, "reviews")
	if err := os.Mkdir(reviewsPath, 0o700); err != nil {
		t.Fatalf("os.Mkdir() error = %v", err)
	}
	corpus := oneCaseCorpus()
	writeJSON(t, corpusPath, corpus)
	writeJSON(t, resultsPath, []review.Result{{
		CaseID:           corpus.Cases[0].ID,
		Status:           review.StatusComplete,
		EngineVersion:    "v0.1.0-test",
		RulePackVersion:  "v0.1.0-test",
		ObservedElements: 1,
		ResolvedElements: 1,
		Findings: []review.Finding{{
			ID: "finding-1", RuleID: "EXPOSURE001", Level: review.LevelWarning,
		}},
	}})
	for _, reviewer := range []struct {
		file string
		name string
		role review.Role
	}{
		{file: "primary.json", name: "codex", role: review.RolePrimary},
		{file: "secondary.json", name: "claude", role: review.RoleSecondary},
	} {
		writeJSON(t, filepath.Join(reviewsPath, reviewer.file), review.Assessment{
			SchemaVersion:       1,
			CaseID:              corpus.Cases[0].ID,
			Reviewer:            reviewer.name,
			Role:                reviewer.role,
			FactuallyCorrect:    true,
			SeverityJustified:   true,
			UsefulBeyondRawDiff: true,
			FindingReviews: []review.FindingReview{{
				FindingID: "finding-1", Correct: true, SeverityJustified: true,
			}},
		})
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Run([]string{
		"benchmark", "score",
		"--corpus", corpusPath,
		"--results", resultsPath,
		"--reviews", reviewsPath,
	}, &stdout, &stderr)

	if code != 0 || !strings.Contains(stdout.String(), `"passed": true`) || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestBenchmarkMaterializeCommandUsesTokenAndWritesCache(t *testing.T) {
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	outputPath := filepath.Join(directory, "cache")
	corpus := oneCaseCorpus()
	writeJSON(t, corpusPath, corpus)
	client := &cliContentClient{files: map[string]githubapi.File{
		strings.Repeat("a", 40): {BlobSHA: "base-blob", Content: []byte("services: {}\n")},
		strings.Repeat("b", 40): {BlobSHA: "head-blob", Content: []byte("services: {}\n")},
	}}
	var receivedToken string
	dependencies := cli.Dependencies{
		Getenv: func(name string) string {
			if name == "GITHUB_TOKEN" {
				return "materialize-token"
			}
			return ""
		},
		NewContentClient: func(token string) benchmark.ContentClient {
			receivedToken = token
			return client
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.RunWithDependencies([]string{
		"benchmark", "materialize", "--corpus", corpusPath, "--out", outputPath,
	}, &stdout, &stderr, dependencies)

	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if receivedToken != "materialize-token" || strings.Contains(stdout.String(), receivedToken) {
		t.Fatalf("token handling failed: received=%q stdout=%q", receivedToken, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"cases": 1`) || !strings.Contains(stdout.String(), `"files": 2`) {
		t.Fatalf("stdout = %q, want materialize summary", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outputPath, corpus.Cases[0].ID, "manifest.json")); err != nil {
		t.Fatalf("manifest stat error = %v", err)
	}
}

func TestBenchmarkMaterializeCommandRejectsArgumentsAndInvalidCorpus(t *testing.T) {
	for _, args := range [][]string{
		{"benchmark", "materialize"},
		{"benchmark", "materialize", "--corpus", "corpus.json"},
		{"benchmark", "materialize", "--out", "cache"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := cli.Run(args, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
			t.Errorf("Run(%#v) code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}

	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	invalid := oneCaseCorpus()
	invalid.SchemaVersion = 2
	writeJSON(t, corpusPath, invalid)
	client := &cliContentClient{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.RunWithDependencies([]string{
		"benchmark", "materialize", "--corpus", corpusPath, "--out", filepath.Join(directory, "cache"),
	}, &stdout, &stderr, cli.Dependencies{NewContentClient: func(string) benchmark.ContentClient { return client }})
	if code != 2 || len(client.calls) != 0 {
		t.Fatalf("code=%d calls=%#v stdout=%q stderr=%q", code, client.calls, stdout.String(), stderr.String())
	}
}

func TestBenchmarkMaterializeCommandPropagatesFetchFailure(t *testing.T) {
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	writeJSON(t, corpusPath, oneCaseCorpus())
	client := &cliContentClient{err: errors.New("fetch unavailable")}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.RunWithDependencies([]string{
		"benchmark", "materialize", "--corpus", corpusPath, "--out", filepath.Join(directory, "cache"),
	}, &stdout, &stderr, cli.Dependencies{NewContentClient: func(string) benchmark.ContentClient { return client }})

	if code != 2 || !strings.Contains(stderr.String(), "fetch unavailable") || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func oneCaseCorpus() benchmark.Corpus {
	return benchmark.Corpus{
		SchemaVersion: 1,
		Cases: []benchmark.Case{{
			ID:          "acme-widget-pr-7",
			Repository:  "https://github.com/acme/widget",
			LicenseURL:  "https://github.com/acme/widget/blob/main/LICENSE",
			LicenseSPDX: "MIT",
			PullRequest: 7,
			MergedAt:    time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
			BaseSHA:     strings.Repeat("a", 40),
			HeadSHA:     strings.Repeat("b", 40),
			Kind:        benchmark.CaseReal,
			Split:       benchmark.SplitTraining,
			Paths:       []string{"compose.yaml"},
		}},
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}

func readJSON(t *testing.T, path string, destination any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if err := json.Unmarshal(contents, destination); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
}

type cliHistoryClient struct{}

func (*cliHistoryClient) ListCommitsForPath(
	_ context.Context,
	_, _ string,
	_, _ string,
	_ int,
) ([]githubapi.Commit, error) {
	return []githubapi.Commit{{SHA: strings.Repeat("c", 40)}}, nil
}

func (*cliHistoryClient) ListPullRequestsForCommit(
	_ context.Context,
	_, _ string,
	_ string,
) ([]githubapi.PullRequest, error) {
	return []githubapi.PullRequest{{
		Number:   7,
		MergedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		BaseSHA:  strings.Repeat("a", 40),
		HeadSHA:  strings.Repeat("b", 40),
	}}, nil
}

type cliContentClient struct {
	files map[string]githubapi.File
	err   error
	calls []string
}

func (client *cliContentClient) GetFile(
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
	file, exists := client.files[sha]
	if !exists {
		return githubapi.File{}, errors.New("missing test file")
	}
	return file, nil
}
