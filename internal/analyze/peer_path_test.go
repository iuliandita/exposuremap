package analyze

import (
	"slices"
	"testing"

	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/report"
)

func TestComparePeerPathsTracksReachablePeerSet(t *testing.T) {
	tests := map[string]struct {
		base graph.Revision
		head graph.Revision
		want []report.Change
	}{
		"new peer": {
			base: normalizedRevision(exposedService("gateway", "inbound")),
			head: normalizedRevision(
				exposedService("gateway", "inbound"),
				graph.Service{Name: "dashboard", Networks: []string{"inbound"}},
			),
			want: []report.Change{addedPeerPath("gateway", "inbound", "dashboard")},
		},
		"final shared network removed": {
			base: normalizedRevision(
				exposedService("gateway", "inbound"),
				graph.Service{Name: "dashboard", Networks: []string{"inbound"}},
			),
			head: normalizedRevision(
				exposedService("gateway", "inbound"),
				graph.Service{Name: "dashboard", Networks: []string{"metrics"}},
			),
			want: []report.Change{removedPeerPath("gateway", "inbound", "dashboard")},
		},
		"second network added": {
			base: normalizedRevision(
				exposedService("gateway", "legacy"),
				graph.Service{Name: "dashboard", Networks: []string{"legacy"}},
			),
			head: normalizedRevision(
				exposedService("gateway", "inbound", "legacy"),
				graph.Service{Name: "dashboard", Networks: []string{"inbound", "legacy"}},
			),
			want: []report.Change{},
		},
		"second network removed": {
			base: normalizedRevision(
				exposedService("gateway", "inbound", "legacy"),
				graph.Service{Name: "dashboard", Networks: []string{"inbound", "legacy"}},
			),
			head: normalizedRevision(
				exposedService("gateway", "legacy"),
				graph.Service{Name: "dashboard", Networks: []string{"legacy"}},
			),
			want: []report.Change{},
		},
		"shared network replaced": {
			base: normalizedRevision(
				exposedService("gateway", "legacy"),
				graph.Service{Name: "dashboard", Networks: []string{"legacy"}},
			),
			head: normalizedRevision(
				exposedService("gateway", "inbound"),
				graph.Service{Name: "dashboard", Networks: []string{"inbound"}},
			),
			want: []report.Change{},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Compare(test.base, test.head).Changes; !slices.Equal(got, test.want) {
				t.Fatalf("changes = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestComparePeerPathsTracksExposureAndServiceLifecycle(t *testing.T) {
	internalSource := graph.Service{Name: "gateway", Networks: []string{"inbound"}}
	exposedSource := exposedService("gateway", "inbound")
	target := graph.Service{Name: "dashboard", Networks: []string{"inbound"}}

	tests := map[string]struct {
		base graph.Revision
		head graph.Revision
		want []report.Change
	}{
		"source becomes exposed": {
			base: normalizedRevision(internalSource, target),
			head: normalizedRevision(exposedSource, target),
			want: []report.Change{addedPeerPath("gateway", "inbound", "dashboard")},
		},
		"source stops being exposed": {
			base: normalizedRevision(exposedSource, target),
			head: normalizedRevision(internalSource, target),
			want: []report.Change{removedPeerPath("gateway", "inbound", "dashboard")},
		},
		"target added": {
			base: normalizedRevision(exposedSource),
			head: normalizedRevision(exposedSource, target),
			want: []report.Change{addedPeerPath("gateway", "inbound", "dashboard")},
		},
		"target removed": {
			base: normalizedRevision(exposedSource, target),
			head: normalizedRevision(exposedSource),
			want: []report.Change{removedPeerPath("gateway", "inbound", "dashboard")},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Compare(test.base, test.head).Changes; !slices.Equal(got, test.want) {
				t.Fatalf("changes = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestComparePeerPathsPreservesPathWhenTargetBecomesExposed(t *testing.T) {
	gateway := exposedService("gateway", "inbound")
	base := normalizedRevision(
		gateway,
		graph.Service{Name: "dashboard", Networks: []string{"inbound"}},
	)
	head := normalizedRevision(gateway, exposedService("dashboard", "inbound"))

	want := []report.Change{addedPeerPath("dashboard", "inbound", "gateway")}
	if got := Compare(base, head).Changes; !slices.Equal(got, want) {
		t.Fatalf("changes = %#v, want reverse addition without false removal %#v", got, want)
	}
}

func TestComparePeerPathsUsesUniqueRenameIdentity(t *testing.T) {
	dashboard := graph.Service{Name: "dashboard", Networks: []string{"inbound"}}
	tests := map[string]struct {
		base graph.Revision
		head graph.Revision
	}{
		"source": {
			base: normalizedRevision(exposedService("old-gateway", "inbound"), dashboard),
			head: normalizedRevision(exposedService("gateway", "inbound"), dashboard),
		},
		"target": {
			base: normalizedRevision(
				exposedService("gateway", "inbound"),
				graph.Service{Name: "old-dashboard", Networks: []string{"inbound"}},
			),
			head: normalizedRevision(exposedService("gateway", "inbound"), dashboard),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Compare(test.base, test.head).Changes; len(got) != 0 {
				t.Fatalf("changes = %#v, want none for unique rename", got)
			}
		})
	}
}

func TestComparePeerPathsLeavesAmbiguousTargetsUnmatched(t *testing.T) {
	gateway := exposedService("gateway", "inbound")
	base := normalizedRevision(
		gateway,
		graph.Service{Name: "old-a", Networks: []string{"inbound"}},
		graph.Service{Name: "old-b", Networks: []string{"inbound"}},
	)
	head := normalizedRevision(
		gateway,
		graph.Service{Name: "new-a", Networks: []string{"inbound"}},
		graph.Service{Name: "new-b", Networks: []string{"inbound"}},
	)
	want := []report.Change{
		addedPeerPath("gateway", "inbound", "new-a"),
		addedPeerPath("gateway", "inbound", "new-b"),
		removedPeerPath("gateway", "inbound", "old-a"),
		removedPeerPath("gateway", "inbound", "old-b"),
	}

	if got := Compare(base, head).Changes; !slices.Equal(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestComparePeerPathsDoesNotCollapseDuplicateSource(t *testing.T) {
	gateway := exposedService("gateway", "inbound")
	dashboard := graph.Service{Name: "dashboard", Networks: []string{"inbound"}}
	base := normalizedRevision(gateway, dashboard)
	head := normalizedRevision(gateway, exposedService("api", "inbound"), dashboard)
	want := []report.Change{
		addedPeerPath("api", "inbound", "dashboard"),
		addedPeerPath("api", "inbound", "gateway"),
		addedPeerPath("gateway", "inbound", "api"),
	}

	if got := Compare(base, head).Changes; !slices.Equal(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestPeerPathOrderingAndRepresentativeNetwork(t *testing.T) {
	source := exposedService("gateway", "zeta", "alpha")
	base := normalizedRevision(source)
	head := normalizedRevision(
		source,
		graph.Service{Name: "z-target", Networks: []string{"zeta", "alpha"}},
		graph.Service{Name: "a-target", Networks: []string{"zeta", "alpha"}},
	)
	wantAdded := []report.Change{
		addedPeerPath("gateway", "alpha", "a-target"),
		addedPeerPath("gateway", "alpha", "z-target"),
	}
	if got := Compare(base, head).Changes; !slices.Equal(got, wantAdded) {
		t.Fatalf("added changes = %#v, want %#v", got, wantAdded)
	}

	base = normalizedRevision(
		source,
		graph.Service{Name: "dashboard", Networks: []string{"zeta", "alpha"}},
	)
	head = normalizedRevision(source)
	wantRemoved := []report.Change{removedPeerPath("gateway", "alpha", "dashboard")}
	if got := Compare(base, head).Changes; !slices.Equal(got, wantRemoved) {
		t.Fatalf("removed changes = %#v, want %#v", got, wantRemoved)
	}
}

func TestComparePeerPathsEmitsBothOrderedExposedPaths(t *testing.T) {
	base := normalizedRevision(
		exposedService("api", "api-only"),
		exposedService("gateway", "gateway-only"),
	)
	head := normalizedRevision(
		exposedService("api", "api-only", "shared"),
		exposedService("gateway", "gateway-only", "shared"),
	)
	want := []report.Change{
		addedPeerPath("api", "shared", "gateway"),
		addedPeerPath("gateway", "shared", "api"),
	}

	if got := Compare(base, head).Changes; !slices.Equal(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestComparePeerPathsCoexistsWithDataServiceFinding(t *testing.T) {
	gateway := exposedService("gateway", "backend")
	base := normalizedRevision(gateway)
	head := normalizedRevision(
		gateway,
		graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"backend"}},
	)

	analysis := Compare(base, head)
	wantChanges := []report.Change{addedPeerPath("gateway", "backend", "db")}
	if !slices.Equal(analysis.Changes, wantChanges) {
		t.Fatalf("changes = %#v, want %#v", analysis.Changes, wantChanges)
	}
	if len(analysis.Findings) != 1 || analysis.Findings[0].RuleID != "EM-COMPOSE-005" {
		t.Fatalf("findings = %#v, want one rule 005 finding", analysis.Findings)
	}
}

func TestBlindMissPeerPaths(t *testing.T) {
	base := normalizedRevision(exposedService("traefik", "inbound"))
	head := normalizedRevision(
		exposedService("traefik", "inbound"),
		graph.Service{Name: "grafana", Image: "grafana/grafana", Networks: []string{"inbound"}},
		graph.Service{Name: "prometheus", Image: "prom/prometheus", Networks: []string{"inbound"}},
	)
	want := []report.Change{
		addedPeerPath("traefik", "inbound", "grafana"),
		addedPeerPath("traefik", "inbound", "prometheus"),
	}

	analysis := Compare(base, head)
	if !slices.Equal(analysis.Changes, want) {
		t.Fatalf("changes = %#v, want %#v", analysis.Changes, want)
	}
	if len(analysis.Findings) != 0 {
		t.Fatalf("findings = %#v, want none", analysis.Findings)
	}
}

func addedPeerPath(source, network, target string) report.Change {
	return report.Change{
		Kind:    report.ChangePeerPathAdded,
		Source:  source,
		Network: network,
		Target:  target,
		Base:    "peer path absent",
		Head:    source + " -> " + network + " -> " + target,
	}
}

func removedPeerPath(source, network, target string) report.Change {
	return report.Change{
		Kind:    report.ChangePeerPathRemoved,
		Source:  source,
		Network: network,
		Target:  target,
		Base:    source + " -> " + network + " -> " + target,
		Head:    "peer path absent",
	}
}
