package githubapi_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/iuliandita/exposuremap/internal/githubapi"
)

func TestListCommitsForPathFollowsPagination(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertHeaders(t, request)
		switch request.URL.Path {
		case "/repos/acme/widget/commits":
			if got := request.URL.Query().Get("sha"); got != "main" {
				t.Errorf("sha = %q, want main", got)
			}
			if got := request.URL.Query().Get("path"); got != "compose.yaml" {
				t.Errorf("path = %q, want compose.yaml", got)
			}
			if got := request.URL.Query().Get("per_page"); got != "3" {
				t.Errorf("per_page = %q, want 3", got)
			}
			response.Header().Set("Link", fmt.Sprintf("<%s/next>; rel=\"next\"", server.URL))
			fmt.Fprint(response, `[
                    {"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
                    {"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
                ]`)
		case "/next":
			fmt.Fprint(response, `[{"sha":"cccccccccccccccccccccccccccccccccccccccc"}]`)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	client := testClient(t, server)
	commits, err := client.ListCommitsForPath(context.Background(), "acme", "widget", "main", "compose.yaml", 3)
	if err != nil {
		t.Fatalf("ListCommitsForPath() error = %v", err)
	}
	want := []githubapi.Commit{
		{SHA: strings.Repeat("a", 40)},
		{SHA: strings.Repeat("b", 40)},
		{SHA: strings.Repeat("c", 40)},
	}
	if !slices.Equal(commits, want) {
		t.Fatalf("commits = %#v, want %#v", commits, want)
	}
}

func TestListCommitsForPathStopsAtLimit(t *testing.T) {
	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests++
		response.Header().Set("Link", fmt.Sprintf("<%s/next>; rel=\"next\"", server.URL))
		fmt.Fprint(response, `[
            {"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
            {"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
        ]`)
	}))
	t.Cleanup(server.Close)

	client := testClient(t, server)
	commits, err := client.ListCommitsForPath(context.Background(), "acme", "widget", "main", "compose.yaml", 1)
	if err != nil {
		t.Fatalf("ListCommitsForPath() error = %v", err)
	}
	if len(commits) != 1 || requests != 1 {
		t.Fatalf("commits = %#v, requests = %d; want one commit and one request", commits, requests)
	}
}

func TestListPullRequestsForCommitSkipsUnmerged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertHeaders(t, request)
		if request.URL.Path != "/repos/acme/widget/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/pulls" {
			http.NotFound(response, request)
			return
		}
		fmt.Fprint(response, `[
            {"number":7,"merged_at":"2026-07-10T12:00:00Z","base":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"head":{"sha":"cccccccccccccccccccccccccccccccccccccccc"}},
            {"number":8,"merged_at":null,"base":{"sha":"dddddddddddddddddddddddddddddddddddddddd"},"head":{"sha":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}
        ]`)
	}))
	t.Cleanup(server.Close)

	client := testClient(t, server)
	pullRequests, err := client.ListPullRequestsForCommit(context.Background(), "acme", "widget", strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("ListPullRequestsForCommit() error = %v", err)
	}
	if len(pullRequests) != 1 || pullRequests[0].Number != 7 {
		t.Fatalf("pull requests = %#v, want merged PR 7", pullRequests)
	}
}

func TestClientReturnsTypedAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		fmt.Fprint(response, `{"message":"rate limited"}`)
	}))
	t.Cleanup(server.Close)

	client := testClient(t, server)
	_, err := client.ListCommitsForPath(context.Background(), "acme", "widget", "main", "compose.yaml", 1)
	var apiError *githubapi.APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiError.StatusCode != http.StatusForbidden || apiError.Message != "rate limited" {
		t.Fatalf("APIError = %#v", apiError)
	}
}

func TestClientRejectsPaginationOutsideAPIOrigin(t *testing.T) {
	outsideRequests := 0
	outside := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		outsideRequests++
		fmt.Fprint(response, `[]`)
	}))
	t.Cleanup(outside.Close)

	api := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Link", fmt.Sprintf("<%s/next>; rel=\"next\"", outside.URL))
		fmt.Fprint(response, `[{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`)
	}))
	t.Cleanup(api.Close)

	client := testClient(t, api)
	_, err := client.ListCommitsForPath(context.Background(), "acme", "widget", "main", "compose.yaml", 2)
	if err == nil || !strings.Contains(err.Error(), "outside configured API origin") {
		t.Fatalf("ListCommitsForPath() error = %v, want cross-origin rejection", err)
	}
	if outsideRequests != 0 {
		t.Fatalf("outside origin received %d requests, want 0", outsideRequests)
	}
}

func TestGetFileFetchesImmutableBoundedContent(t *testing.T) {
	contents := []byte("services:\n  web:\n    image: nginx\n")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertHeaders(t, request)
		if request.URL.Path != "/repos/acme/widget/contents/deploy/compose.yaml" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("ref"); got != strings.Repeat("a", 40) {
			t.Errorf("ref = %q", got)
		}
		fmt.Fprintf(response, `{"type":"file","sha":"blob-sha","size":%d,"encoding":"base64","content":%q}`,
			len(contents), base64.StdEncoding.EncodeToString(contents))
	}))
	t.Cleanup(server.Close)

	client := testClient(t, server)
	file, err := client.GetFile(context.Background(), "acme", "widget", strings.Repeat("a", 40), "deploy/compose.yaml", 1024)
	if err != nil {
		t.Fatalf("GetFile() error = %v", err)
	}
	if file.BlobSHA != "blob-sha" || !slices.Equal(file.Content, contents) {
		t.Fatalf("file = %#v", file)
	}
}

func TestGetFileRejectsUnsafePathBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	client := testClient(t, server)

	for _, path := range []string{"", "/compose.yaml", "../compose.yaml", "deploy/../../compose.yaml", "deploy//compose.yaml"} {
		if _, err := client.GetFile(context.Background(), "acme", "widget", strings.Repeat("a", 40), path, 1024); err == nil {
			t.Errorf("GetFile(path=%q) error = nil", path)
		}
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want zero", requests)
	}
}

func TestGetFileRejectsInvalidOrOversizedContent(t *testing.T) {
	tests := map[string]string{
		"directory":        `{"type":"dir","sha":"x","size":0,"encoding":"none","content":""}`,
		"declared too big": `{"type":"file","sha":"x","size":5,"encoding":"base64","content":"YQ=="}`,
		"decoded too big":  `{"type":"file","sha":"x","size":1,"encoding":"base64","content":"YWJjZGU="}`,
		"wrong encoding":   `{"type":"file","sha":"x","size":1,"encoding":"utf-8","content":"a"}`,
		"malformed base64": `{"type":"file","sha":"x","size":1,"encoding":"base64","content":"%%%"}`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(response, document)
			}))
			t.Cleanup(server.Close)
			client := testClient(t, server)
			if _, err := client.GetFile(context.Background(), "acme", "widget", strings.Repeat("a", 40), "compose.yaml", 4); err == nil {
				t.Fatal("GetFile() error = nil")
			}
		})
	}
}

func TestGetFileBoundsJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(response, `{"type":"file","sha":"x","size":1,"encoding":"base64","content":"%s"}`, strings.Repeat("A", 100_000))
	}))
	t.Cleanup(server.Close)
	client := testClient(t, server)

	_, err := client.GetFile(context.Background(), "acme", "widget", strings.Repeat("a", 40), "compose.yaml", 4)
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("GetFile() error = %v, want bounded-response error", err)
	}
}

func TestGetFileAcceptsGitHubBase64LineBreaksAtLimit(t *testing.T) {
	contents := []byte(strings.Repeat("x", 1024))
	encoded := base64.StdEncoding.EncodeToString(contents)
	var wrapped strings.Builder
	for index, character := range encoded {
		if index > 0 && index%60 == 0 {
			wrapped.WriteByte('\n')
		}
		wrapped.WriteRune(character)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(response, `{"type":"file","sha":"blob","size":%d,"encoding":"base64","content":%q}`,
			len(contents), wrapped.String())
	}))
	t.Cleanup(server.Close)
	client := testClient(t, server)

	file, err := client.GetFile(context.Background(), "acme", "widget", strings.Repeat("a", 40), "compose.yaml", len(contents))

	if err != nil {
		t.Fatalf("GetFile() error = %v", err)
	}
	if !slices.Equal(file.Content, contents) {
		t.Fatalf("decoded content length = %d, want %d", len(file.Content), len(contents))
	}
}

func testClient(t *testing.T, server *httptest.Server) githubapi.Client {
	t.Helper()
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	return githubapi.Client{
		BaseURL:    baseURL,
		Token:      "test-token",
		HTTPClient: server.Client(),
	}
}

func assertHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := request.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got)
	}
	if got := request.Header.Get("X-GitHub-Api-Version"); got != "2026-03-10" {
		t.Errorf("X-GitHub-Api-Version = %q", got)
	}
}
