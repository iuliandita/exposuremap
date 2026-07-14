package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/iuliandita/exposuremap/internal/graph"
)

// ParseOptions contains explicit inputs used while normalizing one revision.
type ParseOptions struct {
	Variables map[string]string
}

// ProjectInput identifies one bounded Compose project revision.
type ProjectInput struct {
	Primary   string
	Root      string
	Overrides []string
	Variables map[string]string
}

// ParseFile safely loads one regular Compose file and normalizes boundary facts.
func ParseFile(ctx context.Context, filePath string) (graph.Revision, error) {
	return ParseProject(ctx, ProjectInput{Primary: filePath})
}

// ParseFileWithOptions safely loads one regular Compose file with explicit variables.
func ParseFileWithOptions(ctx context.Context, filePath string, options ParseOptions) (graph.Revision, error) {
	return ParseProject(ctx, ProjectInput{Primary: filePath, Variables: options.Variables})
}

// ParseProject safely loads an explicit primary file and ordered overrides.
func ParseProject(ctx context.Context, input ProjectInput) (graph.Revision, error) {
	variables := make(map[string]string, len(input.Variables))
	for name, value := range input.Variables {
		variables[name] = value
	}
	overrides := append([]string(nil), input.Overrides...)

	primaryPath, err := filepath.Abs(input.Primary)
	if err != nil || input.Primary == "" {
		return graph.Revision{}, incomplete("prepare Compose project: invalid primary file")
	}
	rootPath := input.Root
	if rootPath == "" {
		rootPath = filepath.Dir(primaryPath)
	} else {
		rootPath, err = filepath.Abs(rootPath)
		if err != nil {
			return graph.Revision{}, incomplete("prepare Compose project: invalid project root")
		}
	}
	entryPaths := append([]string{primaryPath}, overrides...)
	relativePaths := make([]string, len(entryPaths))
	for index, entryPath := range entryPaths {
		absolutePath, absoluteError := filepath.Abs(entryPath)
		if absoluteError != nil {
			return graph.Revision{}, incomplete("prepare Compose project: invalid entry path")
		}
		relativePath, relativeError := pathWithinRoot(rootPath, absolutePath)
		if relativeError != nil {
			return graph.Revision{}, incomplete("prepare Compose project: unsafe entry path")
		}
		relativePaths[index] = relativePath
	}

	snapshot, err := newProjectSnapshot(rootPath)
	if err != nil {
		return graph.Revision{}, incomplete("prepare Compose project: invalid project root")
	}
	closed := false
	defer func() {
		if !closed {
			_ = snapshot.Close()
		}
	}()

	configFiles := make([]types.ConfigFile, 0, len(relativePaths))
	for _, relativePath := range relativePaths {
		content, captureError := snapshot.capture(ctx, relativePath)
		if captureError != nil {
			return graph.Revision{}, incomplete("prepare Compose project: invalid Compose input")
		}
		configFiles = append(configFiles, types.ConfigFile{
			Filename: snapshot.path(relativePath),
			Content:  content,
		})
	}
	if err := collectIncludes(ctx, snapshot, relativePaths[0], relativePaths); err != nil {
		return graph.Revision{}, incomplete("prepare Compose project: invalid Compose import")
	}
	details := types.ConfigDetails{
		WorkingDir:  filepath.Dir(snapshot.path(relativePaths[0])),
		ConfigFiles: configFiles,
		Environment: types.Mapping{},
	}
	revision, parseError := parseProjectDetails(ctx, details, variables, snapshot.allContent())
	cleanupError := snapshot.Close()
	closed = true
	if parseError != nil {
		return graph.Revision{}, parseError
	}
	if cleanupError != nil {
		return graph.Revision{}, incomplete("prepare Compose project: cleanup failed")
	}
	return revision, nil
}

func pathWithinRoot(rootPath, candidatePath string) (string, error) {
	relativePath, err := filepath.Rel(rootPath, candidatePath)
	if err != nil || relativePath == "." || filepath.IsAbs(relativePath) ||
		relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path is outside root")
	}
	return filepath.Clean(relativePath), nil
}
