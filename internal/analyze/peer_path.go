package analyze

import (
	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/report"
)

type peerPair struct {
	source string
	target string
}

type peerPath struct {
	source  string
	network string
	target  string
}

func comparePeerPaths(base, head graph.Revision) []report.Change {
	renamed := pureServiceRenames(base, head)
	basePaths := peerPathIndex(base, renamed)
	headPaths := peerPathIndex(head, nil)
	changes := make([]report.Change, 0)

	for pair, path := range headPaths {
		if _, exists := basePaths[pair]; exists {
			continue
		}
		evidence := path.source + " -> " + path.network + " -> " + path.target
		changes = append(changes, report.Change{
			Kind:    report.ChangePeerPathAdded,
			Source:  path.source,
			Network: path.network,
			Target:  path.target,
			Base:    "peer path absent",
			Head:    evidence,
		})
	}
	for pair, path := range basePaths {
		if _, exists := headPaths[pair]; exists {
			continue
		}
		evidence := path.source + " -> " + path.network + " -> " + path.target
		changes = append(changes, report.Change{
			Kind:    report.ChangePeerPathRemoved,
			Source:  path.source,
			Network: path.network,
			Target:  path.target,
			Base:    evidence,
			Head:    "peer path absent",
		})
	}
	return report.SortChanges(changes)
}

func peerPathIndex(revision graph.Revision, renamed map[string]string) map[peerPair]peerPath {
	result := make(map[peerPair]peerPath)
	for _, source := range revision.Services {
		if source.Reachability != graph.ReachabilityHostExposed {
			continue
		}
		for _, target := range revision.Services {
			if source.Name == target.Name {
				continue
			}
			networks := sharedNetworks(source, target)
			if len(networks) == 0 {
				continue
			}
			pair := peerPair{
				source: canonicalServiceName(source.Name, renamed),
				target: canonicalServiceName(target.Name, renamed),
			}
			result[pair] = peerPath{source: source.Name, network: networks[0], target: target.Name}
		}
	}
	return result
}

func canonicalServiceName(name string, renamed map[string]string) string {
	if renamedName := renamed[name]; renamedName != "" {
		return renamedName
	}
	return name
}
