package compose

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/iuliandita/exposuremap/internal/dockerfile"
	"github.com/iuliandita/exposuremap/internal/graph"
)

// applyBuildFacts enriches services with the resolved final-stage base image of
// the Dockerfile referenced by their build declaration. Enrichment is optional:
// it can only add a base image and never changes completeness or other facts.
func applyBuildFacts(
	ctx context.Context,
	snapshot *projectSnapshot,
	workingDir string,
	skipped map[string]bool,
	project *types.Project,
	revision *graph.Revision,
) error {
	indexes := make(map[string]int, len(revision.Services))
	for index, service := range revision.Services {
		indexes[service.Name] = index
	}
	for _, name := range project.ServiceNames() {
		if err := ctx.Err(); err != nil {
			return incomplete("extract Dockerfile facts: interrupted")
		}
		if skipped[name] {
			continue
		}
		service := project.Services[name]
		if service.Build == nil {
			continue
		}
		index, exists := indexes[name]
		if !exists {
			continue
		}
		if baseImage := resolveBuildImage(ctx, snapshot, workingDir, service.Build); baseImage != "" {
			revision.Services[index].BaseImage = baseImage
		}
	}
	return nil
}

func resolveBuildImage(
	ctx context.Context,
	snapshot *projectSnapshot,
	workingDir string,
	build *types.BuildConfig,
) string {
	if build.Context == "" || build.DockerfileInline != "" || len(build.AdditionalContexts) != 0 {
		return ""
	}
	if unsafeBuildPath(build.Context) || strings.Contains(build.Target, "$") {
		return ""
	}
	name := build.Dockerfile
	if name == "" {
		name = "Dockerfile"
	}
	if unsafeBuildPath(name) {
		return ""
	}
	joined := filepath.Join(workingDir, build.Context, name)
	relative, err := filepath.Rel(snapshot.directory, joined)
	if err != nil {
		return ""
	}
	cleaned, err := cleanRootRelative(relative)
	if err != nil {
		return ""
	}
	content, ok := snapshot.captureOptional(ctx, cleaned)
	if !ok {
		return ""
	}
	args := make(map[string]string, len(build.Args))
	for key, value := range build.Args {
		if value != nil {
			args[key] = *value
		}
	}
	image, ok := dockerfile.Parse(content).FinalImage(build.Target, args)
	if !ok {
		return ""
	}
	return image
}

func unsafeBuildPath(path string) bool {
	return validateStaticImportPath(path) != nil || strings.Contains(path, "git@")
}

// flagUnresolvedBuildInterpolation marks services whose build context,
// dockerfile, or target cannot be resolved from explicit variables. Post-load
// blank substitution would otherwise invent facts from the wrong file.
func flagUnresolvedBuildInterpolation(model map[string]any, lookup template.Mapping, skipped map[string]bool) {
	services, _ := model["services"].(map[string]any)
	for name, raw := range services {
		fields, _ := raw.(map[string]any)
		build, exists := fields["build"]
		if !exists {
			continue
		}
		switch typed := build.(type) {
		case string:
			if unresolvedInterpolation(typed, lookup) {
				skipped[name] = true
			}
		case map[string]any:
			for _, field := range []string{"context", "dockerfile", "target"} {
				if value, exists := typed[field]; exists && containsUnresolvedInterpolation(value, lookup) {
					skipped[name] = true
				}
			}
		}
	}
}
