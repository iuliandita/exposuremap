package compose

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
)

type includeSpec struct {
	Paths            []string
	ProjectDirectory string
}

type extendsSpec struct {
	Service string
	File    string
}

type serviceReference struct {
	File    string
	Service string
}

type configGroup struct {
	WorkingDirectory string
	Paths            []string
}

type importCollector struct {
	snapshot *projectSnapshot
	lookup   template.Mapping
	skipped  map[string]bool
}

// collectImports captures include and extends inputs and reports the services
// whose build declarations contain unresolved interpolation.
func collectImports(
	ctx context.Context,
	snapshot *projectSnapshot,
	primaryPath string,
	entryPaths []string,
	lookup template.Mapping,
) (map[string]bool, error) {
	collector := importCollector{snapshot: snapshot, lookup: lookup, skipped: map[string]bool{}}
	err := collector.collectGroup(ctx, configGroup{
		WorkingDirectory: filepath.Dir(primaryPath),
		Paths:            entryPaths,
	}, 0, map[string]bool{}, map[serviceReference]bool{})
	if err != nil {
		return nil, err
	}
	return collector.skipped, nil
}

func (collector *importCollector) collectGroup(
	ctx context.Context,
	group configGroup,
	depth int,
	activeIncludes map[string]bool,
	activeExtends map[serviceReference]bool,
) error {
	for _, path := range group.Paths {
		if err := collector.collectFile(
			ctx, path, group.WorkingDirectory, depth, activeIncludes, activeExtends,
		); err != nil {
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
	activeIncludes map[string]bool,
	activeExtends map[serviceReference]bool,
) error {
	if activeIncludes[path] {
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
	flagUnresolvedBuildInterpolation(model, collector.lookup, collector.skipped)
	includes, err := parseIncludeSpecs(model["include"])
	if err != nil {
		return err
	}
	if len(includes) != 0 {
		activeIncludes[path] = true
		defer delete(activeIncludes, path)
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
				if err := collector.snapshot.ensureDirectory(ctx, childWorkingDirectory); err != nil {
					return err
				}
			}
			if err := collector.collectGroup(ctx, configGroup{
				WorkingDirectory: childWorkingDirectory,
				Paths:            resolvedPaths,
			}, depth+1, activeIncludes, activeExtends); err != nil {
				return err
			}
		}
	}
	return collector.collectModelExtends(ctx, path, workingDirectory, model, depth, activeExtends)
}

func (collector *importCollector) collectModelExtends(
	ctx context.Context,
	file string,
	workingDirectory string,
	model map[string]any,
	depth int,
	active map[serviceReference]bool,
) error {
	services, _ := model["services"].(map[string]any)
	serviceNames := make([]string, 0, len(services))
	for name := range services {
		serviceNames = append(serviceNames, name)
	}
	slices.Sort(serviceNames)
	for _, service := range serviceNames {
		if err := collector.collectServiceExtends(
			ctx, file, workingDirectory, model, service, depth, active,
		); err != nil {
			return err
		}
	}
	return nil
}

func (collector *importCollector) collectServiceExtends(
	ctx context.Context,
	file string,
	workingDirectory string,
	model map[string]any,
	serviceName string,
	depth int,
	active map[serviceReference]bool,
) error {
	services, _ := model["services"].(map[string]any)
	rawService, exists := services[serviceName]
	if !exists {
		return nil
	}
	service, ok := rawService.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid extends service")
	}
	rawExtends, exists := service["extends"]
	if !exists {
		return nil
	}
	spec, err := parseExtendsSpec(rawExtends)
	if err != nil {
		return err
	}
	if depth >= maxComposeDepth {
		return fmt.Errorf("compose import depth exceeded")
	}
	current := serviceReference{File: file, Service: serviceName}
	if active[current] {
		return fmt.Errorf("extends cycle")
	}
	active[current] = true
	defer delete(active, current)

	if spec.File == "" {
		return collector.collectServiceExtends(
			ctx, file, workingDirectory, model, spec.Service, depth+1, active,
		)
	}
	resolved, err := resolveImportPath(workingDirectory, spec.File)
	if err != nil {
		return err
	}
	content, err := collector.snapshot.capture(ctx, resolved)
	if err != nil {
		return err
	}
	externalModel, err := loadImportModel(ctx, collector.snapshot.path(resolved), content)
	if err != nil {
		return err
	}
	flagUnresolvedBuildInterpolation(externalModel, collector.lookup, collector.skipped)
	return collector.collectServiceExtends(
		ctx, resolved, filepath.Dir(resolved), externalModel, spec.Service, depth+1, active,
	)
}

func parseExtendsSpec(value any) (extendsSpec, error) {
	switch typed := value.(type) {
	case string:
		if typed == "" || strings.Contains(typed, "$") {
			return extendsSpec{}, fmt.Errorf("invalid extends service")
		}
		return extendsSpec{Service: typed}, nil
	case map[string]any:
		for key := range typed {
			if key != "service" && key != "file" {
				return extendsSpec{}, fmt.Errorf("unsupported extends field")
			}
		}
		service, ok := typed["service"].(string)
		if !ok || service == "" || strings.Contains(service, "$") {
			return extendsSpec{}, fmt.Errorf("invalid extends service")
		}
		file := ""
		if rawFile, exists := typed["file"]; exists {
			file, ok = rawFile.(string)
			if !ok || validateStaticImportPath(file) != nil {
				return extendsSpec{}, fmt.Errorf("invalid extends file")
			}
		}
		return extendsSpec{Service: service, File: file}, nil
	default:
		return extendsSpec{}, fmt.Errorf("invalid extends")
	}
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
