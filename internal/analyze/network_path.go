package analyze

import (
	"slices"
	"strings"

	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/report"
)

var dataServiceImages = map[string]struct{}{
	"postgres":          {},
	"postgresql":        {},
	"mysql":             {},
	"mariadb":           {},
	"mongo":             {},
	"mongodb":           {},
	"redis":             {},
	"valkey":            {},
	"elasticsearch":     {},
	"opensearch":        {},
	"meilisearch":       {},
	"clickhouse":        {},
	"clickhouse-server": {},
	"couchdb":           {},
	"influxdb":          {},
	"neo4j":             {},
	"cockroach":         {},
	"cockroachdb":       {},
	"adminer":           {},
	"phpmyadmin":        {},
	"pgadmin4":          {},
}

func dataServiceImage(image string) bool {
	withoutDigest, _, _ := strings.Cut(image, "@")
	basename := withoutDigest
	if slash := strings.LastIndexByte(basename, '/'); slash >= 0 {
		basename = basename[slash+1:]
	}
	basename, _, _ = strings.Cut(basename, ":")
	_, recognized := dataServiceImages[strings.ToLower(basename)]
	return recognized
}

func compareDataServicePaths(base, head graph.Revision) []report.Finding {
	findings := make([]report.Finding, 0)
	renamedServices := pureServiceRenames(base, head)
	for _, source := range head.Services {
		if source.Reachability != graph.ReachabilityHostExposed {
			continue
		}
		for _, target := range head.Services {
			if target.Name == source.Name || target.Reachability != graph.ReachabilityInternal ||
				!dataServiceImage(target.Image) {
				continue
			}
			networks := sharedNetworks(source, target)
			if len(networks) == 0 || equivalentBasePath(base, source, target, renamedServices) {
				continue
			}
			evidence := source.Name + " -> " + networks[0] + " -> " + target.Name + " (" + target.Image + ")"
			findings = append(findings, report.Finding{
				RuleID:      ruleDataService,
				Title:       "Host-exposed service gained a data-service path",
				Severity:    "high",
				Confidence:  "medium",
				Service:     source.Name,
				Evidence:    evidence,
				Base:        "data-service path absent",
				Head:        evidence,
				Remediation: "Isolate the data service on a network the exposed service does not join, or document why the path is required.",
			})
		}
	}
	return findings
}

func equivalentBasePath(
	base graph.Revision,
	headSource, headTarget graph.Service,
	renamedServices map[string]string,
) bool {
	for _, baseSource := range base.Services {
		if baseSource.Reachability != graph.ReachabilityHostExposed ||
			!sameServiceOrPureRename(baseSource, headSource, renamedServices) {
			continue
		}
		for _, baseTarget := range base.Services {
			if baseTarget.Name == baseSource.Name ||
				!sameServiceOrPureRename(baseTarget, headTarget, renamedServices) {
				continue
			}
			if len(sharedNetworks(baseSource, baseTarget)) > 0 {
				return true
			}
		}
	}
	return false
}

func sameServiceOrPureRename(base, head graph.Service, renamedServices map[string]string) bool {
	if base.Name == head.Name {
		return true
	}
	return renamedServices[base.Name] == head.Name
}

func pureServiceRenames(base, head graph.Revision) map[string]string {
	baseNames := serviceNames(base.Services)
	headNames := serviceNames(head.Services)
	baseServices := slices.Clone(base.Services)
	headServices := slices.Clone(head.Services)
	slices.SortFunc(baseServices, compareServiceNames)
	slices.SortFunc(headServices, compareServiceNames)
	unmatchedBase := make([]graph.Service, 0, len(baseServices))
	for _, service := range baseServices {
		if _, unchanged := headNames[service.Name]; !unchanged {
			unmatchedBase = append(unmatchedBase, service)
		}
	}
	unmatchedHead := make([]graph.Service, 0, len(headServices))
	for _, service := range headServices {
		if _, unchanged := baseNames[service.Name]; !unchanged {
			unmatchedHead = append(unmatchedHead, service)
		}
	}

	renamed := make(map[string]string)
	for _, baseService := range unmatchedBase {
		headMatches := matchingBoundaryFacts(baseService, unmatchedHead)
		if len(headMatches) != 1 {
			continue
		}
		baseMatches := matchingBoundaryFacts(headMatches[0], unmatchedBase)
		if len(baseMatches) != 1 {
			continue
		}
		renamed[baseService.Name] = headMatches[0].Name
	}
	return renamed
}

func matchingBoundaryFacts(service graph.Service, candidates []graph.Service) []graph.Service {
	matches := make([]graph.Service, 0)
	for _, candidate := range candidates {
		if sameBoundaryFacts(service, candidate) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func serviceNames(services []graph.Service) map[string]struct{} {
	names := make(map[string]struct{}, len(services))
	for _, service := range services {
		names[service.Name] = struct{}{}
	}
	return names
}

func compareServiceNames(left, right graph.Service) int {
	return strings.Compare(left.Name, right.Name)
}

func sameBoundaryFacts(base, head graph.Service) bool {
	return base.Image == head.Image &&
		slices.Equal(base.Bindings, head.Bindings) &&
		slices.Equal(base.Networks, head.Networks) &&
		slices.Equal(base.Mounts, head.Mounts) &&
		base.Privileged == head.Privileged &&
		base.HostNetwork == head.HostNetwork &&
		base.Reachability == head.Reachability
}

func sharedNetworks(left, right graph.Service) []string {
	shared := make([]string, 0)
	for _, leftNetwork := range left.Networks {
		if slices.Contains(right.Networks, leftNetwork) {
			shared = append(shared, leftNetwork)
		}
	}
	slices.Sort(shared)
	return slices.Compact(shared)
}
