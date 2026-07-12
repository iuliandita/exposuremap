package benchmark

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/iuliandita/exposuremap/internal/githubapi"
)

// HistoryClient is the read-only GitHub history surface needed by Mine.
type HistoryClient interface {
	ListCommitsForPath(
		ctx context.Context,
		owner string,
		repository string,
		branch string,
		path string,
		limit int,
	) ([]githubapi.Commit, error)
	ListPullRequestsForCommit(
		ctx context.Context,
		owner string,
		repository string,
		sha string,
	) ([]githubapi.PullRequest, error)
}

// RelevantPath reports whether a path can affect the supported boundary graph.
func RelevantPath(candidate string) bool {
	base := strings.ToLower(path.Base(candidate))
	extension := path.Ext(base)
	stem := strings.TrimSuffix(base, extension)

	if extension == ".yml" || extension == ".yaml" {
		if stem == "compose" || strings.HasPrefix(stem, "compose.") ||
			stem == "docker-compose" || strings.HasPrefix(stem, "docker-compose.") {
			return true
		}
	}
	if configBase(base, "dockerfile") {
		return true
	}
	if configBase(base, "caddyfile") {
		return true
	}
	if strings.Contains(stem, "traefik") && (extension == ".yml" || extension == ".yaml" || extension == ".toml") {
		return true
	}
	return strings.Contains(stem, "nginx") && extension == ".conf"
}

func configBase(base, name string) bool {
	if base == name {
		return true
	}
	if !strings.HasPrefix(base, name+".") {
		return false
	}
	switch path.Ext(base) {
	case ".md", ".txt", ".rst", ".adoc":
		return false
	default:
		return true
	}
}

// Mine converts path-specific commit history into metadata-only pull-request cases.
func Mine(
	ctx context.Context,
	client HistoryClient,
	seed Seed,
	split Split,
	commitsPerPath int,
	caseLimit int,
) ([]Case, error) {
	if commitsPerPath <= 0 || caseLimit <= 0 {
		return nil, nil
	}
	owner, repository, err := repositoryParts(seed.Repository)
	if err != nil {
		return nil, err
	}

	pathsByCommit := make(map[string]map[string]struct{})
	for _, candidate := range seed.CandidatePaths {
		if !RelevantPath(candidate) {
			continue
		}
		commits, err := client.ListCommitsForPath(
			ctx,
			owner,
			repository,
			seed.DefaultBranch,
			candidate,
			commitsPerPath,
		)
		if err != nil {
			return nil, fmt.Errorf("list commits for %s: %w", candidate, err)
		}
		for _, commit := range commits {
			if commit.SHA == "" {
				continue
			}
			if pathsByCommit[commit.SHA] == nil {
				pathsByCommit[commit.SHA] = make(map[string]struct{})
			}
			pathsByCommit[commit.SHA][candidate] = struct{}{}
		}
	}

	commitSHAs := make([]string, 0, len(pathsByCommit))
	for sha := range pathsByCommit {
		commitSHAs = append(commitSHAs, sha)
	}
	slices.Sort(commitSHAs)

	casesByPullRequest := make(map[int]*Case)
	for _, sha := range commitSHAs {
		pullRequests, err := client.ListPullRequestsForCommit(ctx, owner, repository, sha)
		if err != nil {
			return nil, fmt.Errorf("list pull requests for commit %s: %w", sha, err)
		}
		for _, pullRequest := range pullRequests {
			if pullRequest.Number <= 0 {
				continue
			}
			item := casesByPullRequest[pullRequest.Number]
			if item == nil {
				item = &Case{
					ID:          fmt.Sprintf("%s-pr-%d", caseSlug(owner+"-"+repository), pullRequest.Number),
					Repository:  seed.Repository,
					LicenseURL:  seed.LicenseURL,
					LicenseSPDX: seed.LicenseSPDX,
					PullRequest: pullRequest.Number,
					MergedAt:    pullRequest.MergedAt,
					BaseSHA:     pullRequest.BaseSHA,
					HeadSHA:     pullRequest.HeadSHA,
					Kind:        CaseReal,
					Split:       split,
				}
				casesByPullRequest[pullRequest.Number] = item
			} else if item.BaseSHA != pullRequest.BaseSHA || item.HeadSHA != pullRequest.HeadSHA {
				return nil, fmt.Errorf("pull request %d returned inconsistent base/head SHAs", pullRequest.Number)
			}
			item.Paths = unionPaths(item.Paths, pathsByCommit[sha])
		}
	}

	result := make([]Case, 0, len(casesByPullRequest))
	for _, item := range casesByPullRequest {
		slices.Sort(item.Paths)
		result = append(result, *item)
	}
	slices.SortFunc(result, func(left, right Case) int {
		if left.MergedAt.After(right.MergedAt) {
			return -1
		}
		if left.MergedAt.Before(right.MergedAt) {
			return 1
		}
		return right.PullRequest - left.PullRequest
	})
	if len(result) > caseLimit {
		result = result[:caseLimit]
	}
	return result, nil
}

func repositoryParts(repositoryURL string) (string, string, error) {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" {
		return "", "", fmt.Errorf("invalid GitHub repository URL %q", repositoryURL)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid GitHub repository URL %q", repositoryURL)
	}
	return parts[0], parts[1], nil
}

func unionPaths(existing []string, additions map[string]struct{}) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, candidate := range existing {
		seen[candidate] = struct{}{}
	}
	for candidate := range additions {
		seen[candidate] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for candidate := range seen {
		result = append(result, candidate)
	}
	return result
}

func caseSlug(value string) string {
	var builder strings.Builder
	separator := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			separator = false
			continue
		}
		if builder.Len() > 0 && !separator {
			builder.WriteByte('-')
			separator = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
