package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iuliandita/exposuremap/internal/githubapi"
)

const (
	maxMaterializedPaths = 64
	maxMaterializedFile  = 1 << 20
	maxMaterializedBytes = 8 << 20
	maxManifestBytes     = 1 << 20
)

// ContentClient is the immutable GitHub content surface needed by Materialize.
type ContentClient interface {
	ListFiles(
		ctx context.Context,
		owner string,
		repository string,
		commitSHA string,
		paths []string,
	) (map[string]githubapi.FileMetadata, error)
	GetFile(
		ctx context.Context,
		owner string,
		repository string,
		sha string,
		path string,
		maxBytes int,
	) (githubapi.File, error)
}

// MaterializeSummary describes locally available benchmark source.
type MaterializeSummary struct {
	Cases  int    `json:"cases"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	Output string `json:"output"`
}

// MaterializedManifest records immutable source and local integrity metadata.
type MaterializedManifest struct {
	SchemaVersion int                  `json:"schema_version"`
	CaseID        string               `json:"case_id"`
	Repository    string               `json:"repository"`
	LicenseURL    string               `json:"license_url"`
	LicenseSPDX   string               `json:"license_spdx"`
	Base          MaterializedRevision `json:"base"`
	Head          MaterializedRevision `json:"head"`
}

// MaterializedRevision contains one immutable revision's files.
type MaterializedRevision struct {
	SHA   string             `json:"sha"`
	Files []MaterializedFile `json:"files"`
}

// MaterializedFile records one local file's upstream and local checksums.
type MaterializedFile struct {
	Path    string `json:"path"`
	BlobSHA string `json:"blob_sha"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

// Materialize fetches corpus-listed files into an integrity-checked local cache.
func Materialize(
	ctx context.Context,
	client ContentClient,
	corpus Corpus,
	output string,
) (MaterializeSummary, error) {
	if client == nil {
		return MaterializeSummary{}, fmt.Errorf("content client is required")
	}
	if output == "" {
		return MaterializeSummary{}, fmt.Errorf("output directory is required")
	}
	if problems := Validate(corpus); len(problems) > 0 {
		return MaterializeSummary{}, fmt.Errorf("invalid corpus: %s %s", problems[0].Field, problems[0].Message)
	}
	for _, item := range corpus.Cases {
		if !safeCaseID(item.ID) {
			return MaterializeSummary{}, fmt.Errorf("case %q has unsafe id", item.ID)
		}
		if len(item.Paths) > maxMaterializedPaths {
			return MaterializeSummary{}, fmt.Errorf("case %q exceeds %d paths", item.ID, maxMaterializedPaths)
		}
	}
	if err := ensureCacheDirectory(output); err != nil {
		return MaterializeSummary{}, err
	}

	cases := slices.Clone(corpus.Cases)
	slices.SortFunc(cases, func(left, right Case) int {
		return strings.Compare(left.ID, right.ID)
	})
	summary := MaterializeSummary{Output: output}
	for _, item := range cases {
		manifest, reused, err := existingMaterialization(output, item)
		if err != nil {
			return MaterializeSummary{}, err
		}
		if !reused {
			manifest, err = materializeCase(ctx, client, output, item)
			if err != nil {
				return MaterializeSummary{}, err
			}
		}
		summary.Cases++
		for _, revision := range []MaterializedRevision{manifest.Base, manifest.Head} {
			summary.Files += len(revision.Files)
			for _, file := range revision.Files {
				summary.Bytes += file.Bytes
			}
		}
	}
	return summary, nil
}

// ReadMaterializedManifest decodes one bounded regular manifest file.
func ReadMaterializedManifest(path string) (MaterializedManifest, error) {
	content, err := readBoundedRegular(path, maxManifestBytes)
	if err != nil {
		return MaterializedManifest{}, err
	}
	var manifest MaterializedManifest
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return MaterializedManifest{}, fmt.Errorf("decode materialized manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return MaterializedManifest{}, fmt.Errorf("decode materialized manifest: multiple JSON values")
		}
		return MaterializedManifest{}, fmt.Errorf("decode materialized manifest: %w", err)
	}
	return manifest, nil
}

// VerifyMaterialized reads and verifies one existing case without network access or writes.
func VerifyMaterialized(output string, item Case) (MaterializedManifest, error) {
	if output == "" {
		return MaterializedManifest{}, fmt.Errorf("cache directory is required")
	}
	if problems := Validate(Corpus{SchemaVersion: 1, Cases: []Case{item}}); len(problems) > 0 {
		return MaterializedManifest{}, fmt.Errorf("invalid case: %s %s", problems[0].Field, problems[0].Message)
	}
	if !safeCaseID(item.ID) {
		return MaterializedManifest{}, fmt.Errorf("case %q has unsafe id", item.ID)
	}
	manifest, exists, err := existingMaterialization(output, item)
	if err != nil {
		return MaterializedManifest{}, err
	}
	if !exists {
		return MaterializedManifest{}, fmt.Errorf("materialized case %s does not exist", item.ID)
	}
	return manifest, nil
}

func materializeCase(
	ctx context.Context,
	client ContentClient,
	output string,
	item Case,
) (returnManifest MaterializedManifest, returnError error) {
	owner, repository, err := repositoryParts(item.Repository)
	if err != nil {
		return MaterializedManifest{}, err
	}
	temporary, err := os.MkdirTemp(output, "."+item.ID+"-")
	if err != nil {
		return MaterializedManifest{}, fmt.Errorf("create temporary case directory: %w", err)
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return MaterializedManifest{}, fmt.Errorf("set temporary case permissions: %w", err)
	}
	defer func() {
		if temporary != "" {
			if removeError := os.RemoveAll(temporary); returnError == nil && removeError != nil {
				returnError = fmt.Errorf("remove temporary case directory: %w", removeError)
			}
		}
	}()

	paths := slices.Clone(item.Paths)
	slices.Sort(paths)
	manifest := MaterializedManifest{
		SchemaVersion: 1,
		CaseID:        item.ID,
		Repository:    item.Repository,
		LicenseURL:    item.LicenseURL,
		LicenseSPDX:   item.LicenseSPDX,
		Base:          MaterializedRevision{SHA: item.BaseSHA},
		Head:          MaterializedRevision{SHA: item.HeadSHA},
	}
	var totalBytes int64
	manifest.Base.Files, err = materializeRevision(
		ctx, client, owner, repository, item.BaseSHA, "base", paths, temporary, &totalBytes,
	)
	if err != nil {
		return MaterializedManifest{}, fmt.Errorf("materialize case %s base: %w", item.ID, err)
	}
	manifest.Head.Files, err = materializeRevision(
		ctx, client, owner, repository, item.HeadSHA, "head", paths, temporary, &totalBytes,
	)
	if err != nil {
		return MaterializedManifest{}, fmt.Errorf("materialize case %s head: %w", item.ID, err)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return MaterializedManifest{}, fmt.Errorf("encode materialized manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeExclusiveFile(filepath.Join(temporary, "manifest.json"), encoded); err != nil {
		return MaterializedManifest{}, err
	}
	destination := filepath.Join(output, item.ID)
	if err := os.Rename(temporary, destination); err != nil {
		return MaterializedManifest{}, fmt.Errorf("publish materialized case %s: %w", item.ID, err)
	}
	temporary = ""
	return manifest, nil
}

func materializeRevision(
	ctx context.Context,
	client ContentClient,
	owner string,
	repository string,
	sha string,
	label string,
	paths []string,
	caseDirectory string,
	totalBytes *int64,
) ([]MaterializedFile, error) {
	metadata, err := client.ListFiles(ctx, owner, repository, sha, paths)
	if err != nil {
		return nil, fmt.Errorf("resolve regular files: %w", err)
	}
	files := make([]MaterializedFile, 0, len(paths))
	for _, repositoryPath := range paths {
		expected, exists := metadata[repositoryPath]
		if !exists || expected.BlobSHA == "" {
			return nil, fmt.Errorf("missing Git tree metadata for %s", repositoryPath)
		}
		file, err := client.GetFile(ctx, owner, repository, sha, repositoryPath, maxMaterializedFile)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", repositoryPath, err)
		}
		if file.BlobSHA != expected.BlobSHA || len(file.Content) > maxMaterializedFile {
			return nil, fmt.Errorf("invalid file response for %s", repositoryPath)
		}
		*totalBytes += int64(len(file.Content))
		if *totalBytes > maxMaterializedBytes {
			return nil, fmt.Errorf("case exceeds %d bytes", maxMaterializedBytes)
		}
		destination := filepath.Join(caseDirectory, label, filepath.FromSlash(repositoryPath))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return nil, fmt.Errorf("create materialized path: %w", err)
		}
		if err := writeExclusiveFile(destination, file.Content); err != nil {
			return nil, err
		}
		digest := sha256.Sum256(file.Content)
		files = append(files, MaterializedFile{
			Path:    repositoryPath,
			BlobSHA: file.BlobSHA,
			SHA256:  hex.EncodeToString(digest[:]),
			Bytes:   int64(len(file.Content)),
		})
	}
	return files, nil
}

func existingMaterialization(output string, item Case) (MaterializedManifest, bool, error) {
	caseDirectory := filepath.Join(output, item.ID)
	info, err := os.Lstat(caseDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return MaterializedManifest{}, false, nil
	}
	if err != nil {
		return MaterializedManifest{}, false, fmt.Errorf("inspect cached case %s: %w", item.ID, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return MaterializedManifest{}, false, fmt.Errorf("cached case %s is not a regular directory", item.ID)
	}
	manifestPath := filepath.Join(caseDirectory, "manifest.json")
	manifest, err := ReadMaterializedManifest(manifestPath)
	if err != nil {
		return MaterializedManifest{}, false, fmt.Errorf("read cached case %s: %w", item.ID, err)
	}
	if err := verifyManifest(caseDirectory, item, manifest); err != nil {
		return MaterializedManifest{}, false, fmt.Errorf("verify cached case %s: %w", item.ID, err)
	}
	return manifest, true, nil
}

func verifyManifest(caseDirectory string, item Case, manifest MaterializedManifest) error {
	if manifest.SchemaVersion != 1 || manifest.CaseID != item.ID || manifest.Repository != item.Repository ||
		manifest.LicenseURL != item.LicenseURL || manifest.LicenseSPDX != item.LicenseSPDX ||
		manifest.Base.SHA != item.BaseSHA || manifest.Head.SHA != item.HeadSHA {
		return fmt.Errorf("manifest metadata does not match corpus")
	}
	paths := slices.Clone(item.Paths)
	slices.Sort(paths)
	if err := verifyMaterializedTree(caseDirectory, paths); err != nil {
		return err
	}
	var total int64
	for _, revision := range []struct {
		label string
		value MaterializedRevision
	}{{label: "base", value: manifest.Base}, {label: "head", value: manifest.Head}} {
		if len(revision.value.Files) != len(paths) {
			return fmt.Errorf("%s file list does not match corpus", revision.label)
		}
		for index, file := range revision.value.Files {
			if file.Path != paths[index] || file.BlobSHA == "" || file.Bytes < 0 || !validSHA256(file.SHA256) {
				return fmt.Errorf("%s file metadata does not match corpus", revision.label)
			}
			content, err := readBoundedRegular(
				filepath.Join(caseDirectory, revision.label, filepath.FromSlash(file.Path)),
				maxMaterializedFile,
			)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(content)
			if int64(len(content)) != file.Bytes || hex.EncodeToString(digest[:]) != file.SHA256 {
				return fmt.Errorf("checksum mismatch for %s/%s", revision.label, file.Path)
			}
			total += file.Bytes
		}
	}
	if total > maxMaterializedBytes {
		return fmt.Errorf("cached case exceeds %d bytes", maxMaterializedBytes)
	}
	return nil
}

func verifyMaterializedTree(caseDirectory string, paths []string) error {
	expectedFiles := map[string]struct{}{"manifest.json": {}}
	expectedDirectories := map[string]struct{}{}
	for _, revision := range []string{"base", "head"} {
		expectedDirectories[revision] = struct{}{}
		for _, path := range paths {
			relativePath := filepath.Join(revision, filepath.FromSlash(path))
			expectedFiles[relativePath] = struct{}{}
			for directory := filepath.Dir(relativePath); directory != "."; directory = filepath.Dir(directory) {
				expectedDirectories[directory] = struct{}{}
			}
		}
	}

	return filepath.WalkDir(caseDirectory, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return fmt.Errorf("inspect cache tree: %w", walkError)
		}
		relativePath, err := filepath.Rel(caseDirectory, path)
		if err != nil {
			return fmt.Errorf("inspect cache tree")
		}
		if relativePath == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected cache entry")
		}
		if entry.IsDir() {
			if _, exists := expectedDirectories[relativePath]; !exists {
				return fmt.Errorf("unexpected cache entry")
			}
			return nil
		}
		if _, exists := expectedFiles[relativePath]; !exists {
			return fmt.Errorf("unexpected cache entry")
		}
		return nil
	})
}

func ensureCacheDirectory(output string) error {
	if err := os.MkdirAll(output, 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	info, err := os.Lstat(output)
	if err != nil {
		return fmt.Errorf("inspect cache directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cache path is not a regular directory")
	}
	return nil
}

func writeExclusiveFile(path string, content []byte) (returnError error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	defer func() {
		if closeError := file.Close(); returnError == nil && closeError != nil {
			returnError = fmt.Errorf("close %s: %w", filepath.Base(path), closeError)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set %s permissions: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Base(path), err)
	}
	return nil
}

func readBoundedRegular(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("cached file %s is not a bounded regular 0600 file", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("cached file %s exceeds size limit", filepath.Base(path))
	}
	return content, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safeCaseID(value string) bool {
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, ".") {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}
