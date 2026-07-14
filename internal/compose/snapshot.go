package compose

import (
	"bytes"
	"context"
	"errors"
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
	content, err := readRootedRegularFile(ctx, snapshot.source, relativePath)
	if err != nil {
		return nil, err
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

func readRootedRegularFile(
	ctx context.Context,
	root *os.Root,
	relativePath string,
) (returnContent []byte, returnError error) {
	components := strings.Split(relativePath, string(os.PathSeparator))
	if len(components) == 0 {
		return nil, fmt.Errorf("invalid compose input")
	}
	current := root
	owned := false
	defer func() {
		if owned {
			if err := current.Close(); returnError == nil && err != nil {
				returnContent = nil
				returnError = fmt.Errorf("close compose input directory")
			}
		}
	}()

	for _, component := range components[:len(components)-1] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := current.Lstat(component)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("unsafe compose input path")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return nil, fmt.Errorf("open compose input directory")
		}
		openedInfo, err := next.Lstat(".")
		if err != nil || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			return nil, fmt.Errorf("compose input directory changed while opening")
		}
		if owned {
			if err := current.Close(); err != nil {
				_ = next.Close()
				return nil, fmt.Errorf("close compose input directory")
			}
		}
		current = next
		owned = true
	}

	name := components[len(components)-1]
	info, err := current.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("compose input is not a regular file")
	}
	if info.Size() > maxComposeBytes {
		return nil, fmt.Errorf("compose file limit exceeded")
	}
	file, err := current.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open compose input")
	}
	defer func() {
		if err := file.Close(); returnError == nil && err != nil {
			returnContent = nil
			returnError = fmt.Errorf("close compose input")
		}
	}()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("compose input changed while opening")
	}

	var content bytes.Buffer
	reader := io.LimitReader(file, maxComposeBytes+1)
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readError := reader.Read(buffer)
		if count > 0 {
			_, _ = content.Write(buffer[:count])
		}
		if readError == io.EOF {
			break
		}
		if readError != nil {
			return nil, fmt.Errorf("read compose input")
		}
	}
	if content.Len() > maxComposeBytes {
		return nil, fmt.Errorf("compose file limit exceeded")
	}
	afterInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, afterInfo) ||
		openedInfo.Size() != afterInfo.Size() || openedInfo.Mode() != afterInfo.Mode() ||
		!openedInfo.ModTime().Equal(afterInfo.ModTime()) {
		return nil, fmt.Errorf("compose input changed while reading")
	}
	return content.Bytes(), nil
}

func (snapshot *projectSnapshot) path(relativePath string) string {
	return filepath.Join(snapshot.directory, relativePath)
}

func (snapshot *projectSnapshot) ensureDirectory(ctx context.Context, relativePath string) error {
	if relativePath == "." {
		return nil
	}
	if err := validateRootedDirectory(ctx, snapshot.source, relativePath); err != nil {
		return err
	}
	if err := snapshot.destination.MkdirAll(relativePath, 0o700); err != nil {
		return fmt.Errorf("create project snapshot directory")
	}
	return nil
}

func validateRootedDirectory(
	ctx context.Context,
	root *os.Root,
	relativePath string,
) (returnError error) {
	components := strings.Split(relativePath, string(os.PathSeparator))
	current := root
	owned := false
	defer func() {
		if owned {
			if err := current.Close(); returnError == nil && err != nil {
				returnError = fmt.Errorf("close compose project directory")
			}
		}
	}()

	for _, component := range components {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe compose project directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return fmt.Errorf("open compose project directory")
		}
		openedInfo, err := next.Lstat(".")
		if err != nil || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			return fmt.Errorf("compose project directory changed while opening")
		}
		if owned {
			if err := current.Close(); err != nil {
				_ = next.Close()
				return fmt.Errorf("close compose project directory")
			}
		}
		current = next
		owned = true
	}
	return nil
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
