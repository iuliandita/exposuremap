// Package report defines the stable analyzer output contract.
package report

import (
	"cmp"
	"slices"
)

const (
	// SchemaVersion is the public report schema emitted by this prototype.
	SchemaVersion = 2
	// AnalyzerVersion remains explicit so benchmark runs cannot mix engines silently.
	AnalyzerVersion = "dev"
)

// Status is deliberately warning-only until the benchmark gate passes.
type Status string

const (
	StatusWarn       Status = "warn"
	StatusIncomplete Status = "incomplete"
)

// Source identifies a side of the comparison without exposing a local path.
type Source struct {
	Label string `json:"label"`
}

// Report is the stable output for one comparison.
type Report struct {
	SchemaVersion   int       `json:"schema_version"`
	AnalyzerVersion string    `json:"analyzer_version"`
	Status          Status    `json:"status"`
	Complete        bool      `json:"complete"`
	Base            Source    `json:"base"`
	Head            Source    `json:"head"`
	Diagnostics     []string  `json:"diagnostics,omitempty"`
	Changes         []Change  `json:"changes"`
	Findings        []Finding `json:"findings"`
}

// ChangeKind identifies one informational graph delta.
type ChangeKind string

const (
	ChangePeerPathAdded   ChangeKind = "peer-path-added"
	ChangePeerPathRemoved ChangeKind = "peer-path-removed"
)

// Change describes one informational difference between revisions.
type Change struct {
	Kind    ChangeKind `json:"kind"`
	Source  string     `json:"source"`
	Network string     `json:"network"`
	Target  string     `json:"target"`
	Base    string     `json:"base"`
	Head    string     `json:"head"`
}

// Finding describes one observed boundary change.
type Finding struct {
	RuleID      string `json:"rule_id"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Confidence  string `json:"confidence"`
	Service     string `json:"service"`
	Evidence    string `json:"evidence"`
	Base        string `json:"base"`
	Head        string `json:"head"`
	Remediation string `json:"remediation"`
}

// SortFindings returns a sorted copy suitable for stable rendering.
func SortFindings(findings []Finding) []Finding {
	result := slices.Clone(findings)
	slices.SortFunc(result, func(left, right Finding) int {
		if value := cmp.Compare(left.RuleID, right.RuleID); value != 0 {
			return value
		}
		if value := cmp.Compare(left.Service, right.Service); value != 0 {
			return value
		}
		return cmp.Compare(left.Evidence, right.Evidence)
	})
	return result
}

// SortChanges returns a sorted copy suitable for stable rendering.
func SortChanges(changes []Change) []Change {
	result := slices.Clone(changes)
	slices.SortFunc(result, func(left, right Change) int {
		if value := cmp.Compare(left.Kind, right.Kind); value != 0 {
			return value
		}
		if value := cmp.Compare(left.Source, right.Source); value != 0 {
			return value
		}
		if value := cmp.Compare(left.Target, right.Target); value != 0 {
			return value
		}
		return cmp.Compare(left.Network, right.Network)
	})
	return result
}

// Complete creates a warning-only report for two fully parsed revisions.
func Complete(changes []Change, findings []Finding) Report {
	if changes == nil {
		changes = []Change{}
	}
	if findings == nil {
		findings = []Finding{}
	}
	return Report{
		SchemaVersion:   SchemaVersion,
		AnalyzerVersion: AnalyzerVersion,
		Status:          StatusWarn,
		Complete:        true,
		Base:            Source{Label: "base"},
		Head:            Source{Label: "head"},
		Changes:         SortChanges(changes),
		Findings:        SortFindings(findings),
	}
}

// Incomplete creates a neutral report when either revision cannot be represented.
func Incomplete(diagnostics ...string) Report {
	diagnostics = slices.Clone(diagnostics)
	slices.Sort(diagnostics)
	return Report{
		SchemaVersion:   SchemaVersion,
		AnalyzerVersion: AnalyzerVersion,
		Status:          StatusIncomplete,
		Complete:        false,
		Base:            Source{Label: "base"},
		Head:            Source{Label: "head"},
		Diagnostics:     diagnostics,
		Changes:         []Change{},
		Findings:        []Finding{},
	}
}
