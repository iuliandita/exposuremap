package analyze_test

import (
	"slices"
	"testing"

	"github.com/iuliandita/exposuremap/internal/analyze"
	"github.com/iuliandita/exposuremap/internal/graph"
)

func TestCompareDetectsFiveBoundaryRuleFamilies(t *testing.T) {
	base := revision(
		graph.Service{Name: "web", Bindings: []graph.Binding{{HostIP: "127.0.0.1", Published: "8080", Target: 80}}},
		graph.Service{Name: "agent", Networks: []string{"default"}},
		graph.Service{Name: "admin", Bindings: []graph.Binding{{HostIP: "127.0.0.1", Published: "9000", Target: 9000}}},
		graph.Service{
			Name:     "gateway",
			Bindings: []graph.Binding{{HostIP: "127.0.0.1", Published: "7000", Target: 7000}},
			Networks: []string{"backend"},
		},
	)
	head := revision(
		graph.Service{Name: "web", Bindings: []graph.Binding{
			{HostIP: "0.0.0.0", Published: "8080", Target: 80},
			{HostIP: "127.0.0.1", Published: "9090", Target: 90},
		}},
		graph.Service{Name: "agent", HostNetwork: true},
		graph.Service{
			Name:       "admin",
			Bindings:   []graph.Binding{{HostIP: "127.0.0.1", Published: "9000", Target: 9000}},
			Privileged: true,
			Mounts: []graph.Mount{
				{Type: "bind", Source: "/etc/ssl", Target: "/certs"},
				{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
			},
		},
		graph.Service{
			Name:     "gateway",
			Bindings: []graph.Binding{{HostIP: "127.0.0.1", Published: "7000", Target: 7000}},
			Networks: []string{"backend"},
		},
		graph.Service{Name: "database", Image: "postgres:16-alpine", Networks: []string{"backend"}},
	)

	analysis := analyze.Compare(base, head)
	findings := analysis.Findings
	if analysis.Changes == nil {
		t.Fatal("changes = nil, want stable empty collection")
	}
	gotIDs := make([]string, 0, len(findings))
	for _, finding := range findings {
		gotIDs = append(gotIDs, finding.RuleID)
		wantConfidence := "high"
		if finding.RuleID == "EM-COMPOSE-005" {
			wantConfidence = "medium"
		}
		if finding.Confidence != wantConfidence {
			t.Errorf("finding %#v confidence = %q, want %q", finding, finding.Confidence, wantConfidence)
		}
	}
	wantIDs := []string{
		"EM-COMPOSE-001",
		"EM-COMPOSE-002",
		"EM-COMPOSE-003",
		"EM-COMPOSE-004",
		"EM-COMPOSE-004",
		"EM-COMPOSE-004",
		"EM-COMPOSE-005",
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("rule IDs = %#v, want %#v; findings = %#v", gotIDs, wantIDs, findings)
	}
	if findings[0].Service != "web" || findings[0].Severity != "medium" {
		t.Fatalf("new port finding = %#v", findings[0])
	}
	if findings[1].Service != "web" || findings[1].Severity != "high" {
		t.Fatalf("widening finding = %#v", findings[1])
	}
	if findings[2].Service != "agent" || findings[2].Severity != "high" {
		t.Fatalf("host-network finding = %#v", findings[2])
	}
	if findings[3].Service != "admin" || findings[3].Severity != "medium" {
		t.Fatalf("etc mount finding = %#v", findings[3])
	}
	if findings[4].Severity != "high" || findings[5].Evidence != "privileged: false -> true" {
		t.Fatalf("high-risk findings = [%#v %#v]", findings[4], findings[5])
	}
	if findings[6].Service != "gateway" || findings[6].Severity != "high" {
		t.Fatalf("data-service path finding = %#v", findings[6])
	}
}

func TestCompareDoesNotDuplicateWidenedBindingAsNewPort(t *testing.T) {
	base := revision(graph.Service{Name: "web", Bindings: []graph.Binding{{HostIP: "::1", Published: "8080", Target: 80}}})
	head := revision(graph.Service{Name: "web", Bindings: []graph.Binding{{HostIP: "::", Published: "8080", Target: 80}}})

	findings := analyze.Compare(base, head).Findings

	if len(findings) != 1 || findings[0].RuleID != "EM-COMPOSE-002" {
		t.Fatalf("findings = %#v, want only bind-widening rule", findings)
	}
}

func TestCompareDoesNotReportWideningWhenWildcardAlreadyExisted(t *testing.T) {
	base := revision(graph.Service{Name: "web", Bindings: []graph.Binding{
		{HostIP: "127.0.0.1", Published: "8080", Target: 80},
		{HostIP: "0.0.0.0", Published: "8080", Target: 80},
	}})
	head := revision(graph.Service{Name: "web", Bindings: []graph.Binding{
		{HostIP: "127.0.0.1", Published: "8080", Target: 80},
		{HostIP: "", Published: "8080", Target: 80},
	}})

	if findings := analyze.Compare(base, head).Findings; len(findings) != 0 {
		t.Fatalf("findings = %#v, want none when wildcard reachability already existed", findings)
	}
}

func TestCompareRequiresHostExposureForSensitiveCapability(t *testing.T) {
	base := revision(graph.Service{Name: "worker", Networks: []string{"default"}})
	head := revision(graph.Service{
		Name:       "worker",
		Networks:   []string{"default"},
		Privileged: true,
		Mounts:     []graph.Mount{{Type: "bind", Source: "/etc", Target: "/host-etc"}},
	})

	if findings := analyze.Compare(base, head).Findings; len(findings) != 0 {
		t.Fatalf("findings = %#v, want none for internal service", findings)
	}
}

func TestCompareIgnoresIdenticalAndNarrowingChanges(t *testing.T) {
	wildcard := revision(graph.Service{Name: "web", Bindings: []graph.Binding{{HostIP: "0.0.0.0", Published: "8080", Target: 80}}})
	loopback := revision(graph.Service{Name: "web", Bindings: []graph.Binding{{HostIP: "127.0.0.1", Published: "8080", Target: 80}}})

	if findings := analyze.Compare(wildcard, wildcard).Findings; len(findings) != 0 {
		t.Fatalf("identical findings = %#v, want none", findings)
	}
	if findings := analyze.Compare(wildcard, loopback).Findings; len(findings) != 0 {
		t.Fatalf("narrowing findings = %#v, want none", findings)
	}
}

func TestCompareTreatsExistingSensitiveMountAsUnchanged(t *testing.T) {
	base := revision(graph.Service{
		Name:     "web",
		Bindings: []graph.Binding{{Published: "8080", Target: 80}},
		Mounts:   []graph.Mount{{Type: "bind", Source: "/etc", Target: "/host-etc"}},
	})
	head := revision(graph.Service{
		Name:     "web",
		Bindings: []graph.Binding{{Published: "8080", Target: 80}},
		Mounts:   []graph.Mount{{Type: "bind", Source: "/etc", Target: "/host-etc", ReadOnly: true}},
	})

	if findings := analyze.Compare(base, head).Findings; len(findings) != 0 {
		t.Fatalf("findings = %#v, want none for same mount made read-only", findings)
	}
}

func TestCompareDoesNotTreatPortRenumberAsBoundaryWidening(t *testing.T) {
	base := revision(graph.Service{Name: "redis", Bindings: []graph.Binding{{
		HostIP: "127.0.0.1", Published: "6479", Target: 6379,
	}}})
	head := revision(graph.Service{Name: "redis", Bindings: []graph.Binding{{
		HostIP: "127.0.0.1", Published: "6379", Target: 6379,
	}}})

	if findings := analyze.Compare(base, head).Findings; len(findings) != 0 {
		t.Fatalf("findings = %#v, want none for one-for-one port renumber", findings)
	}
}

func TestComparePreservesHostExposureAcrossServiceRename(t *testing.T) {
	base := revision(
		graph.Service{Name: "backend", Bindings: []graph.Binding{{Published: "8080", Target: 8080}}},
		graph.Service{Name: "frontend", Bindings: []graph.Binding{{Published: "3000", Target: 80}}},
	)
	head := revision(graph.Service{Name: "stirling-pdf", Bindings: []graph.Binding{{Published: "8080", Target: 8080}}})

	if findings := analyze.Compare(base, head).Findings; len(findings) != 0 {
		t.Fatalf("findings = %#v, want none when the host binding survives a service rename", findings)
	}
}

func TestCompareReturnsEmptyAnalysisForIncompleteRevision(t *testing.T) {
	complete := revision(graph.Service{Name: "web", Bindings: []graph.Binding{{Published: "8080", Target: 80}}})
	incomplete := graph.Revision{Services: []graph.Service{{Name: "web", HostNetwork: true}}}

	for name, revisions := range map[string][2]graph.Revision{
		"base": {incomplete, complete},
		"head": {complete, incomplete},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyze.Compare(revisions[0], revisions[1])
			if analysis.Changes == nil || analysis.Findings == nil {
				t.Fatalf("analysis = %#v, want stable empty collections", analysis)
			}
			if len(analysis.Changes) != 0 || len(analysis.Findings) != 0 {
				t.Fatalf("analysis = %#v, want no partial result", analysis)
			}
		})
	}
}

func revision(services ...graph.Service) graph.Revision {
	return graph.Normalize(graph.Revision{Complete: true, Services: services})
}
