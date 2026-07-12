package benchmark_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iuliandita/exposuremap/internal/benchmark"
	"github.com/iuliandita/exposuremap/internal/githubapi"
)

func TestRelevantPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "compose.yaml", want: true},
		{path: "deploy/compose.prod.yml", want: true},
		{path: "docker/docker-compose.yml", want: true},
		{path: "server/Dockerfile", want: true},
		{path: "server/Dockerfile.dev", want: true},
		{path: "proxy/Caddyfile", want: true},
		{path: "proxy/Caddyfile.prod", want: true},
		{path: "proxy/traefik.yml", want: true},
		{path: "proxy/nginx.conf", want: true},
		{path: "README.md", want: false},
		{path: "src/compose.go", want: false},
		{path: "docs/Dockerfile.md", want: false},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := benchmark.RelevantPath(test.path); got != test.want {
				t.Fatalf("RelevantPath(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestMineDeduplicatesAndAggregatesPullRequests(t *testing.T) {
	mergedEarly := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mergedLate := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	client := &fakeHistoryClient{
		commits: map[string][]githubapi.Commit{
			"compose.yaml": {{SHA: strings.Repeat("a", 40)}, {SHA: strings.Repeat("b", 40)}},
			"Dockerfile":   {{SHA: strings.Repeat("a", 40)}},
		},
		pullRequests: map[string][]githubapi.PullRequest{
			strings.Repeat("a", 40): {{
				Number: 7, MergedAt: mergedEarly,
				BaseSHA: strings.Repeat("c", 40), HeadSHA: strings.Repeat("d", 40),
			}},
			strings.Repeat("b", 40): {
				{
					Number: 7, MergedAt: mergedEarly,
					BaseSHA: strings.Repeat("c", 40), HeadSHA: strings.Repeat("d", 40),
				},
				{
					Number: 9, MergedAt: mergedLate,
					BaseSHA: strings.Repeat("e", 40), HeadSHA: strings.Repeat("f", 40),
				},
			},
		},
	}
	seed := benchmark.Seed{
		Repository:     "https://github.com/acme/widget",
		DefaultBranch:  "main",
		CandidatePaths: []string{"compose.yaml", "README.md", "Dockerfile"},
		LicenseURL:     "https://github.com/acme/widget/blob/main/LICENSE",
		LicenseSPDX:    "Apache-2.0",
	}

	cases, err := benchmark.Mine(context.Background(), client, seed, benchmark.SplitTraining, 10, 10)
	if err != nil {
		t.Fatalf("Mine() error = %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("Mine() returned %d cases, want 2: %#v", len(cases), cases)
	}
	if cases[0].PullRequest != 9 || cases[1].PullRequest != 7 {
		t.Fatalf("pull request order = [%d %d], want [9 7]", cases[0].PullRequest, cases[1].PullRequest)
	}
	wantPaths := []string{"Dockerfile", "compose.yaml"}
	if !slices.Equal(cases[1].Paths, wantPaths) {
		t.Fatalf("PR 7 paths = %#v, want %#v", cases[1].Paths, wantPaths)
	}
	if cases[1].LicenseSPDX != "Apache-2.0" || cases[1].Split != benchmark.SplitTraining {
		t.Fatalf("PR 7 metadata = %#v", cases[1])
	}
	if slices.Contains(client.commitCalls, "README.md") {
		t.Fatalf("irrelevant path was requested: %#v", client.commitCalls)
	}
	if len(client.pullRequestCalls) != 2 {
		t.Fatalf("associated-PR calls = %#v, want one per unique commit", client.pullRequestCalls)
	}
}

func TestMineAppliesCaseLimitAfterSorting(t *testing.T) {
	client := &fakeHistoryClient{
		commits: map[string][]githubapi.Commit{
			"compose.yaml": {{SHA: strings.Repeat("a", 40)}, {SHA: strings.Repeat("b", 40)}},
		},
		pullRequests: map[string][]githubapi.PullRequest{
			strings.Repeat("a", 40): {{
				Number: 1, MergedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				BaseSHA: strings.Repeat("c", 40), HeadSHA: strings.Repeat("d", 40),
			}},
			strings.Repeat("b", 40): {{
				Number: 2, MergedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
				BaseSHA: strings.Repeat("e", 40), HeadSHA: strings.Repeat("f", 40),
			}},
		},
	}
	seed := benchmark.Seed{
		Repository:     "https://github.com/acme/widget",
		DefaultBranch:  "main",
		CandidatePaths: []string{"compose.yaml"},
		LicenseURL:     "https://github.com/acme/widget/blob/main/LICENSE",
		LicenseSPDX:    "MIT",
	}

	cases, err := benchmark.Mine(context.Background(), client, seed, benchmark.SplitValidation, 10, 1)
	if err != nil {
		t.Fatalf("Mine() error = %v", err)
	}
	if len(cases) != 1 || cases[0].PullRequest != 2 {
		t.Fatalf("Mine() cases = %#v, want newest PR 2", cases)
	}
}

func TestMinePropagatesHistoryErrors(t *testing.T) {
	want := errors.New("history unavailable")
	client := &fakeHistoryClient{err: want}
	seed := benchmark.Seed{
		Repository:     "https://github.com/acme/widget",
		DefaultBranch:  "main",
		CandidatePaths: []string{"compose.yaml"},
		LicenseURL:     "https://github.com/acme/widget/blob/main/LICENSE",
	}

	_, err := benchmark.Mine(context.Background(), client, seed, benchmark.SplitTraining, 10, 10)
	if !errors.Is(err, want) {
		t.Fatalf("Mine() error = %v, want %v", err, want)
	}
}

func TestMineRejectsDecoratedRepositoryURLBeforeHistoryCalls(t *testing.T) {
	client := &fakeHistoryClient{}
	seed := benchmark.Seed{
		Repository:     "https://github.com/acme/widget?unexpected=true",
		DefaultBranch:  "main",
		CandidatePaths: []string{"compose.yaml"},
		LicenseURL:     "https://github.com/acme/widget/blob/main/LICENSE",
	}

	_, err := benchmark.Mine(context.Background(), client, seed, benchmark.SplitTraining, 10, 10)
	if err == nil || !strings.Contains(err.Error(), "invalid GitHub repository URL") {
		t.Fatalf("Mine() error = %v, want invalid repository URL", err)
	}
	if len(client.commitCalls) != 0 {
		t.Fatalf("history calls = %#v, want none", client.commitCalls)
	}
}

type fakeHistoryClient struct {
	commits          map[string][]githubapi.Commit
	pullRequests     map[string][]githubapi.PullRequest
	err              error
	commitCalls      []string
	pullRequestCalls []string
}

func (client *fakeHistoryClient) ListCommitsForPath(
	_ context.Context,
	_, _ string,
	_ string,
	path string,
	_ int,
) ([]githubapi.Commit, error) {
	client.commitCalls = append(client.commitCalls, path)
	if client.err != nil {
		return nil, client.err
	}
	return slices.Clone(client.commits[path]), nil
}

func (client *fakeHistoryClient) ListPullRequestsForCommit(
	_ context.Context,
	_, _ string,
	sha string,
) ([]githubapi.PullRequest, error) {
	client.pullRequestCalls = append(client.pullRequestCalls, sha)
	if client.err != nil {
		return nil, client.err
	}
	return slices.Clone(client.pullRequests[sha]), nil
}
