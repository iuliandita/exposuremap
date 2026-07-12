// Package report defines the stable analyzer output contract.
package report

import (
	"cmp"
	"slices"
)

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
