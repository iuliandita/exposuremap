package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iuliandita/exposuremap/internal/report"
)

func TestRenderJSONIsDeterministicAndSortsFindings(t *testing.T) {
	input := report.Complete(
		[]report.Change{
			{Kind: report.ChangePeerPathRemoved, Source: "z", Network: "omega", Target: "db", Base: "z -> omega -> db", Head: "peer path absent"},
			{Kind: report.ChangePeerPathAdded, Source: "a", Network: "alpha", Target: "api", Base: "peer path absent", Head: "a -> alpha -> api"},
		},
		[]report.Finding{
			{RuleID: "EM-COMPOSE-003", Title: "Host mode", Service: "z", Evidence: "host", Severity: "high", Confidence: "high"},
			{RuleID: "EM-COMPOSE-001", Title: "New port", Service: "a", Evidence: "*:80", Severity: "medium", Confidence: "high"},
		},
	)

	first := renderJSON(t, input)
	second := renderJSON(t, input)

	if first != second {
		t.Fatalf("JSON differs between runs:\nfirst: %s\nsecond: %s", first, second)
	}
	if !strings.Contains(first, `"schema_version": 2`) {
		t.Fatalf("schema 2 missing from %s", first)
	}
	if strings.Index(first, `"peer-path-added"`) > strings.Index(first, `"peer-path-removed"`) {
		t.Fatalf("changes are not sorted: %s", first)
	}
	if strings.Index(first, "EM-COMPOSE-001") > strings.Index(first, "EM-COMPOSE-003") {
		t.Fatalf("findings are not sorted: %s", first)
	}
	for _, forbidden := range []string{"/home/", "timestamp", "generated_at"} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("JSON contains %q: %s", forbidden, first)
		}
	}
	if !strings.HasSuffix(first, "\n") || !strings.Contains(first, `"status": "warn"`) || !strings.Contains(first, `"complete": true`) {
		t.Fatalf("JSON contract missing from %s", first)
	}
}

func TestReportCollectionsEncodeAsEmptyArrays(t *testing.T) {
	for name, value := range map[string]report.Report{
		"complete":   report.Complete(nil, nil),
		"incomplete": report.Incomplete("unsupported"),
	} {
		t.Run(name, func(t *testing.T) {
			encoded := renderJSON(t, value)
			var document map[string]any
			if err := json.Unmarshal([]byte(encoded), &document); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			for _, field := range []string{"changes", "findings"} {
				items, ok := document[field].([]any)
				if !ok || items == nil || len(items) != 0 {
					t.Fatalf("%s = %#v, want empty array", field, document[field])
				}
			}
		})
	}
}

func TestRenderMarkdownIsStableForCompleteReport(t *testing.T) {
	input := report.Complete(nil, []report.Finding{{
		RuleID: "EM-COMPOSE-001", Title: "New host port published", Severity: "medium", Confidence: "high",
		Service: "web", Evidence: "*:8080->80/tcp", Base: "binding absent", Head: "*:8080->80/tcp",
		Remediation: "Remove the binding.",
	}})

	first := renderMarkdown(t, input)
	second := renderMarkdown(t, input)

	if first != second {
		t.Fatal("Markdown differs between runs")
	}
	wantParts := []string{
		"# ExposureMap report\n",
		"Status: `warn`",
		"Analysis: complete",
		"## EM-COMPOSE-001: New host port published",
		"- Service: `web`",
		"- Evidence: `*:8080->80/tcp`",
		"### Remediation\n\nRemove the binding.",
	}
	for _, want := range wantParts {
		if !strings.Contains(first, want) {
			t.Errorf("Markdown missing %q:\n%s", want, first)
		}
	}
	if !strings.HasSuffix(first, "\n") {
		t.Fatal("Markdown does not end with newline")
	}
}

func TestRenderMarkdownShowsIncompleteDiagnostics(t *testing.T) {
	input := report.Incomplete("head: unsupported Compose construct: include", "base: malformed Compose file")

	output := renderMarkdown(t, input)

	if !strings.Contains(output, "Status: `incomplete`") || !strings.Contains(output, "Analysis: incomplete") {
		t.Fatalf("incomplete status missing:\n%s", output)
	}
	if strings.Index(output, "base: malformed") > strings.Index(output, "head: unsupported") {
		t.Fatalf("diagnostics are not sorted:\n%s", output)
	}
}

func TestCompleteReportWithNoFindingsRemainsWarningOnly(t *testing.T) {
	output := renderMarkdown(t, report.Complete(nil, nil))

	if !strings.Contains(output, "Status: `warn`") || !strings.Contains(output, "No boundary widening was detected within observed inputs.") {
		t.Fatalf("empty complete report =\n%s", output)
	}
}

func renderJSON(t *testing.T, value report.Report) string {
	t.Helper()
	var output bytes.Buffer
	if err := report.RenderJSON(&output, value); err != nil {
		t.Fatalf("RenderJSON() error = %v", err)
	}
	return output.String()
}

func renderMarkdown(t *testing.T, value report.Report) string {
	t.Helper()
	var output bytes.Buffer
	if err := report.RenderMarkdown(&output, value); err != nil {
		t.Fatalf("RenderMarkdown() error = %v", err)
	}
	return output.String()
}
