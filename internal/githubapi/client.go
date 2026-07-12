package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiVersion     = "2026-03-10"
	defaultBaseURL = "https://api.github.com/"
	maxErrorBytes  = 1 << 20
)

// Client is the narrow GitHub REST client used by corpus acquisition.
type Client struct {
	BaseURL    *url.URL
	Token      string
	HTTPClient *http.Client
}

// PullRequest contains only the immutable metadata needed for a corpus case.
type PullRequest struct {
	Number   int
	MergedAt time.Time
	BaseSHA  string
	HeadSHA  string
}

// Commit identifies one immutable repository revision.
type Commit struct {
	SHA string `json:"sha"`
}

// APIError is a non-success response from GitHub.
type APIError struct {
	StatusCode int
	Message    string
}

func (err *APIError) Error() string {
	return fmt.Sprintf("GitHub API returned %d: %s", err.StatusCode, err.Message)
}

// ListCommitsForPath returns at most limit commits touching one path.
func (client Client) ListCommitsForPath(
	ctx context.Context,
	owner string,
	repository string,
	branch string,
	filePath string,
	limit int,
) ([]Commit, error) {
	if limit <= 0 {
		return nil, nil
	}
	endpoint, err := client.endpoint("repos", owner, repository, "commits")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("sha", branch)
	query.Set("path", filePath)
	query.Set("per_page", strconv.Itoa(min(limit, 100)))
	endpoint.RawQuery = query.Encode()

	result := make([]Commit, 0, limit)
	next := endpoint.String()
	for next != "" && len(result) < limit {
		var page []Commit
		next, err = client.get(ctx, next, &page)
		if err != nil {
			return nil, err
		}
		remaining := limit - len(result)
		if len(page) > remaining {
			page = page[:remaining]
		}
		result = append(result, page...)
	}
	return result, nil
}

// ListPullRequestsForCommit returns merged pull requests associated with a commit.
func (client Client) ListPullRequestsForCommit(ctx context.Context, owner, repository, sha string) ([]PullRequest, error) {
	endpoint, err := client.endpoint("repos", owner, repository, "commits", sha, "pulls")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("per_page", "100")
	endpoint.RawQuery = query.Encode()

	var result []PullRequest
	next := endpoint.String()
	for next != "" {
		var page []struct {
			Number   int        `json:"number"`
			MergedAt *time.Time `json:"merged_at"`
			Base     struct {
				SHA string `json:"sha"`
			} `json:"base"`
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		next, err = client.get(ctx, next, &page)
		if err != nil {
			return nil, err
		}
		for _, item := range page {
			if item.MergedAt == nil {
				continue
			}
			result = append(result, PullRequest{
				Number:   item.Number,
				MergedAt: *item.MergedAt,
				BaseSHA:  item.Base.SHA,
				HeadSHA:  item.Head.SHA,
			})
		}
	}
	return result, nil
}

func (client Client) endpoint(parts ...string) (*url.URL, error) {
	baseURL, err := client.configuredBaseURL()
	if err != nil {
		return nil, err
	}
	copyURL := *baseURL
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	copyURL.Path = strings.TrimSuffix(copyURL.Path, "/") + "/" + strings.Join(escaped, "/")
	copyURL.RawQuery = ""
	copyURL.Fragment = ""
	return &copyURL, nil
}

func (client Client) get(ctx context.Context, target string, destination any) (string, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parse GitHub API request URL: %w", err)
	}
	baseURL, err := client.configuredBaseURL()
	if err != nil {
		return "", err
	}
	if targetURL.Scheme != baseURL.Scheme || !strings.EqualFold(targetURL.Host, baseURL.Host) || targetURL.User != nil {
		return "", fmt.Errorf("GitHub API pagination URL is outside configured API origin")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("create GitHub API request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	if client.Token != "" {
		request.Header.Set("Authorization", "Bearer "+client.Token)
	}

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("send GitHub API request: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", decodeAPIError(response)
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		return "", fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nextLink(response.Header.Values("Link")), nil
}

func (client Client) configuredBaseURL() (*url.URL, error) {
	if client.BaseURL != nil {
		return client.BaseURL, nil
	}
	parsed, err := url.Parse(defaultBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse default GitHub API URL: %w", err)
	}
	return parsed, nil
}

func decodeAPIError(response *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
	if err != nil {
		return fmt.Errorf("read GitHub API error response: %w", err)
	}
	message := strings.TrimSpace(string(body))
	var document struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &document) == nil && document.Message != "" {
		message = document.Message
	}
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return &APIError{StatusCode: response.StatusCode, Message: message}
}

func nextLink(headers []string) string {
	for _, header := range headers {
		for _, entry := range strings.Split(header, ",") {
			parts := strings.Split(entry, ";")
			if len(parts) < 2 || !strings.Contains(strings.Join(parts[1:], ";"), `rel="next"`) {
				continue
			}
			return strings.Trim(strings.TrimSpace(parts[0]), "<>")
		}
	}
	return ""
}
