package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iuliandita/exposuremap/internal/cli"
)

func TestRunDiffRendersWarningOnlyJSON(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", composeWithPort("127.0.0.1"))
	head := writeCLICompose(t, directory, "head.yaml", composeWithPort("0.0.0.0"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{"diff", "--base", base, "--head", head, "--format", "json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want warning-only 0; stderr = %q", code, stderr.String())
	}
	for _, want := range []string{`"status": "warn"`, `"complete": true`, `"rule_id": "EM-COMPOSE-002"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunDiffRendersMarkdown(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", "services:\n  web:\n    image: nginx\n")
	head := writeCLICompose(t, directory, "head.yaml", composeWithPort("127.0.0.1"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{"diff", "--base", base, "--head", head, "--format", "markdown"}, &stdout, &stderr)

	if code != 0 || !strings.Contains(stdout.String(), "## EM-COMPOSE-001: New host port published") {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunDiffReportsIncompleteWithoutLeakingLocalPath(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "missing.yaml")
	head := writeCLICompose(t, directory, "head.yaml", composeWithPort("127.0.0.1"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{"diff", "--base", base, "--head", head}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want neutral 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "incomplete"`) {
		t.Fatalf("stdout = %q, want incomplete report", stdout.String())
	}
	if strings.Contains(stdout.String(), directory) {
		t.Fatalf("stdout leaks local directory %q: %s", directory, stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunDiffRejectsInvalidArguments(t *testing.T) {
	tests := [][]string{
		{"diff"},
		{"diff", "--base", "a", "--head", "b", "--format", "yaml"},
		{"diff", "--base", "a", "--head", "b", "extra"},
	}
	for _, args := range tests {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := cli.Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("Run(%#v) code = %d, want 2", args, code)
		}
		if stderr.Len() == 0 {
			t.Errorf("Run(%#v) stderr is empty", args)
		}
	}
}

func TestRunHelpMentionsDiff(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{"help"}, &stdout, &stderr)

	if code != 0 || !strings.Contains(stdout.String(), "exposuremap diff") {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func writeCLICompose(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func composeWithPort(host string) string {
	return "services:\n  web:\n    image: nginx\n    ports:\n      - \"" + host + ":8080:80\"\n"
}
