package report

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// RenderJSON writes an indented deterministic JSON report.
func RenderJSON(writer io.Writer, value Report) error {
	value = normalizedReport(value)
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// RenderMarkdown writes a deterministic human-readable report.
func RenderMarkdown(writer io.Writer, value Report) error {
	value = normalizedReport(value)
	var output strings.Builder
	fmt.Fprintln(&output, "# ExposureMap report")
	fmt.Fprintln(&output)
	fmt.Fprintf(&output, "Status: `%s`\n\n", value.Status)
	analysis := "incomplete"
	if value.Complete {
		analysis = "complete"
	}
	fmt.Fprintf(&output, "Analysis: %s\n\n", analysis)
	fmt.Fprintf(&output, "Findings: %d\n", len(value.Findings))

	if len(value.Diagnostics) > 0 {
		fmt.Fprintln(&output, "\n## Diagnostics")
		for _, diagnostic := range value.Diagnostics {
			fmt.Fprintf(&output, "\n- %s", singleLine(diagnostic))
		}
		fmt.Fprintln(&output)
	}

	if len(value.Findings) == 0 {
		fmt.Fprintln(&output, "\n## Findings")
		fmt.Fprintln(&output)
		if value.Complete {
			fmt.Fprintln(&output, "No boundary widening was detected within observed inputs.")
		} else {
			fmt.Fprintln(&output, "No findings were evaluated because analysis is incomplete.")
		}
	} else {
		for _, finding := range value.Findings {
			fmt.Fprintf(&output, "\n## %s: %s\n\n", singleLine(finding.RuleID), singleLine(finding.Title))
			fmt.Fprintf(&output, "- Severity: `%s`\n", inlineCode(finding.Severity))
			fmt.Fprintf(&output, "- Confidence: `%s`\n", inlineCode(finding.Confidence))
			fmt.Fprintf(&output, "- Service: `%s`\n", inlineCode(finding.Service))
			fmt.Fprintf(&output, "- Evidence: `%s`\n", inlineCode(finding.Evidence))
			fmt.Fprintf(&output, "- Base: %s\n", singleLine(finding.Base))
			fmt.Fprintf(&output, "- Head: %s\n", singleLine(finding.Head))
			fmt.Fprintln(&output, "\n### Remediation")
			fmt.Fprintf(&output, "\n%s\n", singleLine(finding.Remediation))
		}
	}
	_, err := io.WriteString(writer, output.String())
	return err
}

func normalizedReport(value Report) Report {
	value.Findings = SortFindings(value.Findings)
	if value.Findings == nil {
		value.Findings = []Finding{}
	}
	value.Diagnostics = slices.Clone(value.Diagnostics)
	slices.Sort(value.Diagnostics)
	return value
}

func inlineCode(value string) string {
	return strings.ReplaceAll(singleLine(value), "`", "'")
}

func singleLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
