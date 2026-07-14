package compose

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

type includeSpec struct {
	Paths            []string
	ProjectDirectory string
}

type configGroup struct {
	WorkingDirectory string
	Paths            []string
}

type importCollector struct {
	snapshot *projectSnapshot
}

func collectIncludes(
	ctx context.Context,
	snapshot *projectSnapshot,
	primaryPath string,
	entryPaths []string,
) error {
	collector := importCollector{snapshot: snapshot}
	return collector.collectGroup(ctx, configGroup{
		WorkingDirectory: filepath.Dir(primaryPath),
		Paths:            entryPaths,
	}, 0, map[string]bool{})
}

func (collector *importCollector) collectGroup(
	ctx context.Context,
	group configGroup,
	depth int,
	active map[string]bool,
) error {
	for _, path := range group.Paths {
		if err := collector.collectFile(ctx, path, group.WorkingDirectory, depth, active); err != nil {
			return err
		}
	}
	return nil
}

func (collector *importCollector) collectFile(
	ctx context.Context,
	path string,
	workingDirectory string,
	depth int,
	active map[string]bool,
) error {
	if active[path] {
		return fmt.Errorf("include cycle")
	}
	content, err := collector.snapshot.capture(ctx, path)
	if err != nil {
		return err
	}
	model, err := loadImportModel(ctx, collector.snapshot.path(path), content)
	if err != nil {
		return err
	}
	includes, err := parseIncludeSpecs(model["include"])
	if err != nil {
		return err
	}
	if len(includes) == 0 {
		return nil
	}
	active[path] = true
	defer delete(active, path)
	for _, include := range includes {
		if depth >= maxComposeDepth {
			return fmt.Errorf("compose import depth exceeded")
		}
		resolvedPaths := make([]string, len(include.Paths))
		for index, includePath := range include.Paths {
			resolved, resolveError := resolveImportPath(workingDirectory, includePath)
			if resolveError != nil {
				return resolveError
			}
			resolvedPaths[index] = resolved
		}
		childWorkingDirectory := filepath.Dir(resolvedPaths[0])
		if include.ProjectDirectory != "" {
			childWorkingDirectory, err = resolveImportDirectory(workingDirectory, include.ProjectDirectory)
			if err != nil {
				return err
			}
			if err := collector.snapshot.ensureDirectory(childWorkingDirectory); err != nil {
				return err
			}
		}
		if err := collector.collectGroup(ctx, configGroup{
			WorkingDirectory: childWorkingDirectory,
			Paths:            resolvedPaths,
		}, depth+1, active); err != nil {
			return err
		}
	}
	return nil
}

func loadImportModel(ctx context.Context, filename string, content []byte) (map[string]any, error) {
	details := types.ConfigDetails{
		WorkingDir: filepath.Dir(filename),
		ConfigFiles: []types.ConfigFile{{
			Filename: filename,
			Content:  content,
		}},
		Environment: types.Mapping{},
	}
	model, err := loader.LoadModelWithContext(ctx, details, func(options *loader.Options) {
		options.ResolvePaths = false
		options.SkipInclude = true
		options.SkipExtends = true
		options.SkipResolveEnvironment = true
		options.SkipInterpolation = true
		options.SkipValidation = true
		options.SkipConsistencyCheck = true
		options.SkipDefaultValues = true
		options.SkipNormalization = true
		options.SetProjectName("exposuremap", true)
	})
	if err != nil {
		return nil, fmt.Errorf("invalid compose import model")
	}
	return model, nil
}

func parseIncludeSpecs(value any) ([]includeSpec, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid include")
	}
	result := make([]includeSpec, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			paths, err := parseImportPaths(typed)
			if err != nil {
				return nil, err
			}
			result = append(result, includeSpec{Paths: paths})
		case map[string]any:
			for key := range typed {
				if key != "path" && key != "project_directory" {
					return nil, fmt.Errorf("unsupported include field")
				}
			}
			paths, err := parseImportPaths(typed["path"])
			if err != nil {
				return nil, err
			}
			projectDirectory := ""
			if rawDirectory, exists := typed["project_directory"]; exists {
				projectDirectory, ok = rawDirectory.(string)
				if !ok || validateStaticImportPath(projectDirectory) != nil {
					return nil, fmt.Errorf("invalid include project directory")
				}
			}
			result = append(result, includeSpec{Paths: paths, ProjectDirectory: projectDirectory})
		default:
			return nil, fmt.Errorf("invalid include")
		}
	}
	return result, nil
}

func parseImportPaths(value any) ([]string, error) {
	var paths []string
	switch typed := value.(type) {
	case string:
		paths = []string{typed}
	case []any:
		paths = make([]string, len(typed))
		for index, item := range typed {
			path, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("invalid include path")
			}
			paths[index] = path
		}
	default:
		return nil, fmt.Errorf("invalid include path")
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("empty include path")
	}
	for _, path := range paths {
		if err := validateStaticImportPath(path); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func validateStaticImportPath(path string) error {
	if path == "" || strings.Contains(path, "$") || filepath.IsAbs(path) ||
		strings.Contains(path, "://") || strings.HasPrefix(path, `\\`) ||
		(len(path) >= 2 && path[1] == ':') {
		return fmt.Errorf("unsafe compose import path")
	}
	return nil
}

func resolveImportPath(workingDirectory, path string) (string, error) {
	if err := validateStaticImportPath(path); err != nil {
		return "", err
	}
	return cleanRootRelative(filepath.Join(workingDirectory, path))
}

func resolveImportDirectory(workingDirectory, path string) (string, error) {
	if err := validateStaticImportPath(path); err != nil {
		return "", err
	}
	return cleanRootRelative(filepath.Join(workingDirectory, path))
}

func cleanRootRelative(path string) (string, error) {
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe compose import path")
	}
	return cleaned, nil
}
