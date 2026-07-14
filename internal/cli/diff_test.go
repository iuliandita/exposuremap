package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iuliandita/exposuremap/internal/cli"
	"github.com/iuliandita/exposuremap/internal/report"
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

func TestRunDiffRendersPeerPathJSON(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", composeWithPeer(false))
	head := writeCLICompose(t, directory, "head.yaml", composeWithPeer(true))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{"diff", "--base", base, "--head", head, "--format", "json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want warning-only 0; stderr = %q", code, stderr.String())
	}
	var document report.Report
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, stdout.String())
	}
	if document.SchemaVersion != 2 || document.Status != report.StatusWarn || !document.Complete {
		t.Fatalf("report contract = %#v", document)
	}
	if len(document.Changes) != 1 || document.Changes[0].Kind != report.ChangePeerPathAdded {
		t.Fatalf("changes = %#v, want one peer-path addition", document.Changes)
	}
	if len(document.Findings) != 0 {
		t.Fatalf("findings = %#v, want none", document.Findings)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunDiffRendersPeerPathMarkdown(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", composeWithPeer(false))
	head := writeCLICompose(t, directory, "head.yaml", composeWithPeer(true))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{"diff", "--base", base, "--head", head, "--format", "markdown"}, &stdout, &stderr)

	for _, want := range []string{
		"Changes: 1",
		"Findings: 0",
		"- `peer-path-added`: `gateway -> inbound -> dashboard`",
		"No warning-level boundary widening was detected within observed inputs.",
	} {
		if code != 0 || !strings.Contains(stdout.String(), want) {
			t.Fatalf("code = %d, stdout missing %q: %q; stderr = %q", code, want, stdout.String(), stderr.String())
		}
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
	for _, want := range []string{`"changes": []`, `"findings": []`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %s", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String(), directory) {
		t.Fatalf("stdout leaks local directory %q: %s", directory, stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunDiffUsesExplicitContext(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", `
services:
  web:
    image: nginx
    volumes:
      - ${DATA_ROOT}:/data
`)
	head := writeCLICompose(t, directory, "head.yaml", `
services:
  web:
    image: nginx
    volumes:
      - ${DATA_ROOT}:/data
    ports:
      - ${WEB_PORT}:80
`)
	contextPath := writeCLICompose(t, directory, ".exposuremap.yml", `
schema_version: 1
variables:
  common:
    DATA_ROOT: ./data
  head:
    WEB_PORT: "8080"
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{
		"diff", "--base", base, "--head", head, "--context", contextPath, "--format", "json",
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want warning-only 0; stderr = %q", code, stderr.String())
	}
	for _, want := range []string{`"status": "warn"`, `"complete": true`, `"rule_id": "EM-COMPOSE-001"`, `8080`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunDiffRejectsInvalidContextWithoutLeakingPathOrValue(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", "services: {}\n")
	head := writeCLICompose(t, directory, "head.yaml", "services: {}\n")
	malformed := writeCLICompose(t, directory, "bad.yml", "schema_version: 1\nunknown: DO_NOT_PRINT\n")

	for name, contextPath := range map[string]string{
		"malformed": malformed,
		"missing":   filepath.Join(directory, "missing.yml"),
	} {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			code := cli.Run([]string{
				"diff", "--base", base, "--head", head, "--context", contextPath,
			}, &stdout, &stderr)

			if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "load context:") {
				t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}
			for _, forbidden := range []string{directory, "DO_NOT_PRINT"} {
				if strings.Contains(stderr.String(), forbidden) {
					t.Fatalf("stderr leaks %q: %s", forbidden, stderr.String())
				}
			}
		})
	}
}

func TestRunDiffRejectsInvalidArguments(t *testing.T) {
	tests := [][]string{
		{"diff"},
		{"diff", "--base", "a", "--head", "b", "--format", "yaml"},
		{"diff", "--base", "a", "--head", "b", "--base-override"},
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

	for _, want := range []string{
		"exposuremap diff", "--base-root PATH", "--head-root PATH",
		"--base-override PATH ...", "--head-override PATH ...", "--context PATH",
	} {
		if code != 0 || !strings.Contains(stdout.String(), want) {
			t.Fatalf("code = %d, stdout missing %q: %q, stderr = %q", code, want, stdout.String(), stderr.String())
		}
	}
}

func TestRunDiffUsesIndependentRootsAndOrderedOverrides(t *testing.T) {
	directory := t.TempDir()
	baseRoot := filepath.Join(directory, "base")
	headRoot := filepath.Join(directory, "head")
	base := writeCLIProjectFile(t, baseRoot, "app/compose.yaml", "services:\n  web:\n    image: nginx\n")
	baseOverride := writeCLIProjectFile(t, baseRoot, "overrides/base.yaml", "services:\n  web:\n    image: nginx:base\n")
	head := writeCLIProjectFile(t, headRoot, "app/compose.yaml", "services:\n  web:\n    image: nginx\n")
	headFirst := writeCLIProjectFile(t, headRoot, "overrides/first.yaml", `
services:
  web:
    image: nginx:first
    ports:
      - 8080:80
`)
	headSecond := writeCLIProjectFile(t, headRoot, "overrides/second.yaml", `
services:
  web:
    image: nginx:second
    ports: !override
      - 9090:90
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{
		"diff",
		"--base", base, "--base-root", baseRoot, "--base-override", baseOverride,
		"--head", head, "--head-root", headRoot,
		"--head-override", headFirst, "--head-override", headSecond,
	}, &stdout, &stderr)

	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		`"status": "warn"`, `"rule_id": "EM-COMPOSE-001"`, `"evidence": "*:9090-\u003e90/tcp"`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %s", want, stdout.String())
		}
	}
}

func TestRunDiffAppliesContextInsideIncludes(t *testing.T) {
	directory := t.TempDir()
	baseRoot := filepath.Join(directory, "base")
	headRoot := filepath.Join(directory, "head")
	base := writeCLIProjectFile(t, baseRoot, "compose.yaml", "include:\n  - child.yaml\n")
	writeCLIProjectFile(t, baseRoot, "child.yaml", "services:\n  web:\n    image: nginx\n")
	head := writeCLIProjectFile(t, headRoot, "compose.yaml", "include:\n  - child.yaml\n")
	writeCLIProjectFile(t, headRoot, "child.yaml", `
services:
  web:
    image: nginx
    ports:
      - ${WEB_PORT}:80
`)
	contextPath := writeCLIProjectFile(t, directory, "context.yml", `
schema_version: 1
variables:
  head:
    WEB_PORT: "8080"
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{
		"diff", "--base", base, "--base-root", baseRoot,
		"--head", head, "--head-root", headRoot, "--context", contextPath,
	}, &stdout, &stderr)

	if code != 0 || !strings.Contains(stdout.String(), `"rule_id": "EM-COMPOSE-001"`) || stderr.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunDiffReportsUnsafeRootAsSanitizedIncomplete(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "root")
	base := writeCLIProjectFile(t, root, "compose.yaml", "include:\n  - ../DO_NOT_PRINT.yaml\n")
	head := writeCLIProjectFile(t, root, "head.yaml", "services: {}\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Run([]string{
		"diff", "--base", base, "--base-root", root, "--head", head, "--head-root", root,
	}, &stdout, &stderr)

	if code != 0 || !strings.Contains(stdout.String(), `"status": "incomplete"`) || stderr.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	for _, forbidden := range []string{directory, "DO_NOT_PRINT", "exposuremap-compose-"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("stdout leaks %q: %s", forbidden, stdout.String())
		}
	}
}

func writeCLIProjectFile(t *testing.T, root, relativePath, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
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

func composeWithPeer(includeDashboard bool) string {
	contents := "services:\n" +
		"  gateway:\n" +
		"    image: example/gateway:1\n" +
		"    ports:\n" +
		"      - \"127.0.0.1:8080:80\"\n" +
		"    networks: [inbound]\n"
	if includeDashboard {
		contents += "  dashboard:\n" +
			"    image: grafana/grafana:latest\n" +
			"    networks: [inbound]\n"
	}
	return contents + "networks:\n  inbound: {}\n"
}
