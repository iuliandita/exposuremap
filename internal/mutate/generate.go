// Package mutate creates deterministic controlled benchmark mutations.
package mutate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/iuliandita/exposuremap/internal/benchmark"
	"github.com/iuliandita/exposuremap/internal/evaluate"
	"github.com/iuliandita/exposuremap/internal/review"
)

const (
	maxSourceBytes      = 1 << 20
	maxMutationsPerRule = 100
)

var ruleIDs = []string{
	"EM-COMPOSE-001",
	"EM-COMPOSE-002",
	"EM-COMPOSE-003",
	"EM-COMPOSE-004",
	"EM-COMPOSE-005",
}

// Case contains the local bytes and manifest for one controlled mutation.
type Case struct {
	Item     benchmark.Case
	Manifest benchmark.MaterializedManifest
	Path     string
	Base     []byte
	Head     []byte
}

// Bundle is an atomically publishable mutation corpus and source cache.
type Bundle struct {
	Corpus benchmark.Corpus
	Cases  []Case
}

type source struct {
	item    benchmark.Case
	path    string
	content []byte
}

type serviceMutation struct {
	suffix string
	base   *yaml.Node
	head   *yaml.Node
}

// Generate derives countPerRule isolated mutations from complete training cases.
func Generate(
	ctx context.Context,
	cache string,
	corpus benchmark.Corpus,
	countPerRule int,
) (Bundle, error) {
	if countPerRule <= 0 {
		return Bundle{}, fmt.Errorf("count per rule must be positive")
	}
	if countPerRule > maxMutationsPerRule {
		return Bundle{}, fmt.Errorf("count per rule must be at most %d", maxMutationsPerRule)
	}
	sources, err := completeSources(ctx, cache, corpus)
	if err != nil {
		return Bundle{}, err
	}
	if len(sources) == 0 {
		return Bundle{}, fmt.Errorf("no complete training Compose revisions are available")
	}

	bundle := Bundle{Corpus: benchmark.Corpus{SchemaVersion: 1}}
	for ruleIndex, ruleID := range ruleIDs {
		for index := range countPerRule {
			source := sources[(ruleIndex*countPerRule+index)%len(sources)]
			ordinal := index + 1
			serviceName := fmt.Sprintf("exposuremap_mutation_%03d_%02d", ruleIndex+1, ordinal)
			base, head, err := mutateDocument(source.content, serviceName, ruleID, ordinal)
			if err != nil {
				return Bundle{}, fmt.Errorf("mutate source case %s for %s: %w", source.item.ID, ruleID, err)
			}
			item := mutationCase(source, ruleID, ordinal, base, head)
			generated := Case{
				Item: item,
				Path: source.path,
				Base: base,
				Head: head,
			}
			generated.Manifest = mutationManifest(generated)
			bundle.Corpus.Cases = append(bundle.Corpus.Cases, item)
			bundle.Cases = append(bundle.Cases, generated)
		}
	}
	if problems := benchmark.Validate(bundle.Corpus); len(problems) > 0 {
		return Bundle{}, fmt.Errorf("generated invalid corpus: %s %s", problems[0].Field, problems[0].Message)
	}
	return bundle, nil
}

func completeSources(ctx context.Context, cache string, corpus benchmark.Corpus) ([]source, error) {
	if problems := benchmark.Validate(corpus); len(problems) > 0 {
		return nil, fmt.Errorf("invalid source corpus: %s %s", problems[0].Field, problems[0].Message)
	}
	cases := slices.Clone(corpus.Cases)
	slices.SortFunc(cases, func(left, right benchmark.Case) int {
		return strings.Compare(left.ID, right.ID)
	})
	result := make([]source, 0, len(cases))
	for _, item := range cases {
		if item.Split != benchmark.SplitTraining {
			return nil, fmt.Errorf("source case %s is not in the training split", item.ID)
		}
		outcome, err := evaluate.RunCase(ctx, cache, item)
		if err != nil {
			return nil, err
		}
		if outcome.Result.Status != review.StatusComplete {
			continue
		}
		composePaths := benchmark.ComposeCases([]benchmark.Case{item})
		if len(composePaths) != 1 {
			continue
		}
		var composePath string
		for _, candidate := range item.Paths {
			if benchmark.ComposePath(candidate) {
				composePath = candidate
				break
			}
		}
		content, err := readRegular(
			filepath.Join(cache, item.ID, "head", filepath.FromSlash(composePath)),
		)
		if err != nil {
			return nil, fmt.Errorf("read source case %s: %w", item.ID, err)
		}
		result = append(result, source{item: item, path: composePath, content: content})
	}
	return result, nil
}

func mutateDocument(content []byte, serviceName, ruleID string, ordinal int) ([]byte, []byte, error) {
	base, err := decodeDocument(content)
	if err != nil {
		return nil, nil, err
	}
	head, err := decodeDocument(content)
	if err != nil {
		return nil, nil, err
	}
	baseServices, err := servicesMapping(&base)
	if err != nil {
		return nil, nil, err
	}
	headServices, err := servicesMapping(&head)
	if err != nil {
		return nil, nil, err
	}
	networkName := ""
	if ruleID == "EM-COMPOSE-005" {
		baseNetworks, err := documentMapping(&base, "networks", true)
		if err != nil {
			return nil, nil, err
		}
		headNetworks, err := documentMapping(&head, "networks", true)
		if err != nil {
			return nil, nil, err
		}
		networkName = collisionFreeMappingName(baseNetworks, headNetworks, serviceName+"_network")
		appendMapping(baseNetworks, scalar(networkName), mapping())
		appendMapping(headNetworks, scalar(networkName), mapping())
	}
	port := 41000 + ordinal
	services, err := mutationServices(ruleID, port, ordinal, networkName)
	if err != nil {
		return nil, nil, err
	}
	for _, service := range services {
		name := collisionFreeMappingName(baseServices, headServices, serviceName+service.suffix)
		appendMapping(baseServices, scalar(name), service.base)
		appendMapping(headServices, scalar(name), service.head)
	}
	baseBytes, err := encodeDocument(&base)
	if err != nil {
		return nil, nil, err
	}
	headBytes, err := encodeDocument(&head)
	if err != nil {
		return nil, nil, err
	}
	return baseBytes, headBytes, nil
}

func mutationServices(ruleID string, port, ordinal int, networkName string) ([]serviceMutation, error) {
	image := field("image", scalar("scratch"))
	loopbackPort := fmt.Sprintf("127.0.0.1:%d:8080", port)
	wildcardPort := fmt.Sprintf("0.0.0.0:%d:8080", port)
	switch ruleID {
	case "EM-COMPOSE-001":
		baseFields := append(slices.Clone(image), field("network_mode", scalar("none"))...)
		headFields := append(slices.Clone(baseFields), field("ports", sequence(scalar(wildcardPort)))...)
		return []serviceMutation{{
			base: mapping(baseFields...),
			head: mapping(headFields...),
		}}, nil
	case "EM-COMPOSE-002":
		return []serviceMutation{{
			base: mapping(append(slices.Clone(image), field("ports", sequence(scalar(loopbackPort)))...)...),
			head: mapping(append(slices.Clone(image), field("ports", sequence(scalar(wildcardPort)))...)...),
		}}, nil
	case "EM-COMPOSE-003":
		baseFields := append(slices.Clone(image), field("network_mode", scalar("none"))...)
		return []serviceMutation{{
			base: mapping(baseFields...),
			head: mapping(append(slices.Clone(image), field("network_mode", scalar("host"))...)...),
		}}, nil
	case "EM-COMPOSE-004":
		baseFields := append(slices.Clone(image), field("ports", sequence(scalar(loopbackPort)))...)
		headFields := slices.Clone(baseFields)
		if ordinal%2 == 1 {
			headFields = append(headFields, field("privileged", boolean(true))...)
		} else {
			headFields = append(headFields, field(
				"volumes",
				sequence(scalar("/var/run/docker.sock:/var/run/docker.sock:ro")),
			)...)
		}
		return []serviceMutation{{base: mapping(baseFields...), head: mapping(headFields...)}}, nil
	case "EM-COMPOSE-005":
		sourceFields := append(slices.Clone(image), field("ports", sequence(scalar(loopbackPort)))...)
		baseSourceFields := append(slices.Clone(sourceFields), field("network_mode", scalar("none"))...)
		headSourceFields := append(slices.Clone(sourceFields), field("networks", sequence(scalar(networkName)))...)
		targetFields := append(
			field("image", scalar("postgres:16-alpine")),
			field("networks", sequence(scalar(networkName)))...,
		)
		return []serviceMutation{
			{base: mapping(baseSourceFields...), head: mapping(headSourceFields...)},
			{suffix: "_target", base: mapping(targetFields...), head: mapping(slices.Clone(targetFields)...)},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported mutation rule %s", ruleID)
	}
}

func mutationCase(source source, ruleID string, ordinal int, base, head []byte) benchmark.Case {
	ruleSlug := strings.ToLower(ruleID)
	return benchmark.Case{
		ID:               fmt.Sprintf("%s-mutation-%s-%02d", source.item.ID, ruleSlug, ordinal),
		Repository:       source.item.Repository,
		LicenseURL:       source.item.LicenseURL,
		LicenseSPDX:      source.item.LicenseSPDX,
		BaseSHA:          contentID(base),
		HeadSHA:          contentID(head),
		Kind:             benchmark.CaseMutation,
		Split:            benchmark.SplitTraining,
		Paths:            []string{source.path},
		ExpectedBoundary: ruleID,
	}
}

func mutationManifest(item Case) benchmark.MaterializedManifest {
	return benchmark.MaterializedManifest{
		SchemaVersion: 1,
		CaseID:        item.Item.ID,
		Repository:    item.Item.Repository,
		LicenseURL:    item.Item.LicenseURL,
		LicenseSPDX:   item.Item.LicenseSPDX,
		Base: benchmark.MaterializedRevision{
			SHA: item.Item.BaseSHA,
			Files: []benchmark.MaterializedFile{
				materializedFile(item.Path, item.Base),
			},
		},
		Head: benchmark.MaterializedRevision{
			SHA: item.Item.HeadSHA,
			Files: []benchmark.MaterializedFile{
				materializedFile(item.Path, item.Head),
			},
		},
	}
}

func materializedFile(path string, content []byte) benchmark.MaterializedFile {
	digest := sha256.Sum256(content)
	return benchmark.MaterializedFile{
		Path:    path,
		BlobSHA: contentID(content),
		SHA256:  hex.EncodeToString(digest[:]),
		Bytes:   int64(len(content)),
	}
}

func contentID(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])[:40]
}

func decodeDocument(content []byte) (yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return yaml.Node{}, fmt.Errorf("decode Compose YAML: %w", err)
	}
	return document, nil
}

func encodeDocument(document *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode Compose YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close Compose YAML encoder: %w", err)
	}
	return output.Bytes(), nil
}

func servicesMapping(document *yaml.Node) (*yaml.Node, error) {
	return documentMapping(document, "services", false)
}

func documentMapping(document *yaml.Node, fieldName string, create bool) (*yaml.Node, error) {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("Compose YAML root is not a mapping")
	}
	root := document.Content[0]
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value != fieldName {
			continue
		}
		value := root.Content[index+1]
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("Compose %s is not a mapping", fieldName)
		}
		return value, nil
	}
	if !create {
		return nil, fmt.Errorf("Compose %s mapping is missing", fieldName)
	}
	value := mapping()
	appendMapping(root, scalar(fieldName), value)
	return value, nil
}

func mappingHasKey(mapping *yaml.Node, key string) bool {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return true
		}
	}
	return false
}

func collisionFreeMappingName(base, head *yaml.Node, requested string) string {
	if !mappingHasKey(base, requested) && !mappingHasKey(head, requested) {
		return requested
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s_%d", requested, suffix)
		if !mappingHasKey(base, candidate) && !mappingHasKey(head, candidate) {
			return candidate
		}
	}
}

func appendMapping(mapping *yaml.Node, nodes ...*yaml.Node) {
	mapping.Content = append(mapping.Content, nodes...)
}

func field(name string, value *yaml.Node) []*yaml.Node {
	return []*yaml.Node{scalar(name), value}
}

func mapping(nodes ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: nodes}
}

func sequence(nodes ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: nodes}
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func boolean(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", value)}
}

// WriteBundle atomically publishes a mutation corpus and case cache.
func WriteBundle(output string, bundle Bundle) (returnError error) {
	if output == "" {
		return fmt.Errorf("output directory is required")
	}
	output = filepath.Clean(output)
	if _, err := os.Lstat(output); err == nil {
		return fmt.Errorf("output directory already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, "."+filepath.Base(output)+"-")
	if err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	defer func() {
		if temporary != "" {
			if err := os.RemoveAll(temporary); returnError == nil && err != nil {
				returnError = err
			}
		}
	}()
	if err := writeJSON(filepath.Join(temporary, "corpus.json"), bundle.Corpus); err != nil {
		return err
	}
	casesDirectory := filepath.Join(temporary, "cases")
	if err := os.Mkdir(casesDirectory, 0o700); err != nil {
		return err
	}
	for _, item := range bundle.Cases {
		caseDirectory := filepath.Join(casesDirectory, item.Item.ID)
		for _, revision := range []struct {
			name    string
			content []byte
		}{{name: "base", content: item.Base}, {name: "head", content: item.Head}} {
			path := filepath.Join(caseDirectory, revision.name, filepath.FromSlash(item.Path))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := writeFile(path, bytes.NewReader(revision.content)); err != nil {
				return err
			}
		}
		if err := writeJSON(filepath.Join(caseDirectory, "manifest.json"), item.Manifest); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, output); err != nil {
		return err
	}
	temporary = ""
	return nil
}

func writeJSON(path string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return writeFile(path, bytes.NewReader(content))
}

func writeFile(path string, content io.Reader) (returnError error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); returnError == nil && err != nil {
			returnError = err
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := io.Copy(file, content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return nil
}

func readRegular(path string) (returnContent []byte, returnError error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSourceBytes {
		return nil, fmt.Errorf("source is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); returnError == nil && err != nil {
			returnError = err
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("source changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxSourceBytes {
		return nil, fmt.Errorf("source exceeds size limit")
	}
	return content, nil
}
