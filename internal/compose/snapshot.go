package compose

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxComposeFiles      = 64
	maxComposeBytes      = 1 << 20
	maxComposeTotalBytes = 8 << 20
	maxComposeDepth      = 16
)

type projectSnapshot struct {
	source      *os.Root
	destination *os.Root
	directory   string
	files       map[string][]byte
	totalBytes  int
}

func newProjectSnapshot(rootPath string) (*projectSnapshot, error) {
	rootInfo, err := os.Lstat(rootPath)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("invalid project root")
	}
	source, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open project root")
	}
	openedInfo, err := source.Lstat(".")
	if err != nil || !os.SameFile(rootInfo, openedInfo) {
		_ = source.Close()
		return nil, fmt.Errorf("project root changed while opening")
	}

	directory, err := os.MkdirTemp("", "exposuremap-compose-")
	if err != nil {
		_ = source.Close()
		return nil, fmt.Errorf("create project snapshot")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = source.Close()
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("secure project snapshot")
	}
	destination, err := os.OpenRoot(directory)
	if err != nil {
		_ = source.Close()
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("open project snapshot")
	}
	return &projectSnapshot{
		source: source, destination: destination, directory: directory, files: map[string][]byte{},
	}, nil
}

func (snapshot *projectSnapshot) capture(ctx context.Context, relativePath string) ([]byte, error) {
	if content, exists := snapshot.files[relativePath]; exists {
		return content, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filepath.Base(relativePath) == ".env" {
		return nil, fmt.Errorf("reserved compose filename")
	}
	if len(snapshot.files) >= maxComposeFiles {
		return nil, fmt.Errorf("compose file limit exceeded")
	}
	info, err := snapshot.source.Lstat(relativePath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("compose input is not a regular file")
	}
	if info.Size() > maxComposeBytes {
		return nil, fmt.Errorf("compose file limit exceeded")
	}
	file, err := snapshot.source.Open(relativePath)
	if err != nil {
		return nil, fmt.Errorf("open compose input")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("compose input changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxComposeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read compose input")
	}
	if len(content) > maxComposeBytes || snapshot.totalBytes+len(content) > maxComposeTotalBytes {
		return nil, fmt.Errorf("compose file limit exceeded")
	}
	parent := filepath.Dir(relativePath)
	if parent != "." {
		if err := snapshot.destination.MkdirAll(parent, 0o700); err != nil {
			return nil, fmt.Errorf("create project snapshot directory")
		}
	}
	if err := snapshot.destination.WriteFile(relativePath, content, 0o600); err != nil {
		return nil, fmt.Errorf("write project snapshot")
	}
	if err := snapshot.destination.Chmod(relativePath, 0o600); err != nil {
		return nil, fmt.Errorf("secure project snapshot file")
	}
	snapshot.files[relativePath] = content
	snapshot.totalBytes += len(content)
	return content, nil
}

func (snapshot *projectSnapshot) path(relativePath string) string {
	return filepath.Join(snapshot.directory, relativePath)
}

func (snapshot *projectSnapshot) allContent() []byte {
	paths := make([]string, 0, len(snapshot.files))
	for path := range snapshot.files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var result []byte
	for _, path := range paths {
		result = append(result, snapshot.files[path]...)
		result = append(result, '\n')
	}
	return result
}

func (snapshot *projectSnapshot) Close() error {
	var closeErrors []string
	if snapshot.destination != nil {
		if err := snapshot.destination.Close(); err != nil {
			closeErrors = append(closeErrors, "close snapshot")
		}
		snapshot.destination = nil
	}
	if snapshot.source != nil {
		if err := snapshot.source.Close(); err != nil {
			closeErrors = append(closeErrors, "close source")
		}
		snapshot.source = nil
	}
	if snapshot.directory != "" {
		if err := os.RemoveAll(snapshot.directory); err != nil {
			closeErrors = append(closeErrors, "remove snapshot")
		} else {
			snapshot.directory = ""
		}
	}
	if len(closeErrors) != 0 {
		return fmt.Errorf("%s", strings.Join(closeErrors, ", "))
	}
	return nil
}
