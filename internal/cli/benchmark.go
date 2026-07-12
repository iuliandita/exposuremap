package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iuliandita/exposuremap/internal/benchmark"
	"github.com/iuliandita/exposuremap/internal/githubapi"
	"github.com/iuliandita/exposuremap/internal/review"
)

// Dependencies contains the external state used by benchmark acquisition.
type Dependencies struct {
	Getenv           func(string) string
	NewHistoryClient func(token string) benchmark.HistoryClient
	NewContentClient func(token string) benchmark.ContentClient
}

func defaultDependencies() Dependencies {
	return Dependencies{
		Getenv: os.Getenv,
		NewHistoryClient: func(token string) benchmark.HistoryClient {
			return &githubapi.Client{Token: token}
		},
		NewContentClient: func(token string) benchmark.ContentClient {
			return &githubapi.Client{Token: token}
		},
	}
}

func normalizeDependencies(dependencies Dependencies) Dependencies {
	defaults := defaultDependencies()
	if dependencies.Getenv == nil {
		dependencies.Getenv = defaults.Getenv
	}
	if dependencies.NewHistoryClient == nil {
		dependencies.NewHistoryClient = defaults.NewHistoryClient
	}
	if dependencies.NewContentClient == nil {
		dependencies.NewContentClient = defaults.NewContentClient
	}
	return dependencies
}

func runBenchmark(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "benchmark subcommand is required: validate, mine, materialize, or score")
		return 2
	}
	switch args[0] {
	case "validate":
		return runBenchmarkValidate(args[1:], stdout, stderr)
	case "mine":
		return runBenchmarkMine(args[1:], stdout, stderr, dependencies)
	case "materialize":
		return runBenchmarkMaterialize(args[1:], stdout, stderr, dependencies)
	case "score":
		return runBenchmarkScore(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown benchmark subcommand %q\n", args[0])
		return 2
	}
}

func runBenchmarkMaterialize(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	flags := flag.NewFlagSet("benchmark materialize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusPath := flags.String("corpus", "", "path to a corpus JSON file")
	outputPath := flags.String("out", "", "directory for immutable case files")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *corpusPath == "" || *outputPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "benchmark materialize requires --corpus PATH --out DIR")
		return 2
	}
	var corpus benchmark.Corpus
	if err := decodeJSONFile(*corpusPath, &corpus); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	client := dependencies.NewContentClient(dependencies.Getenv("GITHUB_TOKEN"))
	summary, err := benchmark.Materialize(context.Background(), client, corpus, *outputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := encodeJSON(stdout, summary); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

func runBenchmarkValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("benchmark validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusPath := flags.String("corpus", "", "path to a corpus JSON file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *corpusPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "benchmark validate requires --corpus PATH")
		return 2
	}

	var corpus benchmark.Corpus
	if err := decodeJSONFile(*corpusPath, &corpus); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	problems := benchmark.Validate(corpus)
	response := struct {
		Valid    bool                `json:"valid"`
		Problems []benchmark.Problem `json:"problems"`
	}{
		Valid:    len(problems) == 0,
		Problems: problems,
	}
	if err := encodeJSON(stdout, response); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !response.Valid {
		return 1
	}
	return 0
}

func runBenchmarkMine(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	flags := flag.NewFlagSet("benchmark mine", flag.ContinueOnError)
	flags.SetOutput(stderr)
	seedsPath := flags.String("seeds", "", "path to a seed manifest")
	outputPath := flags.String("out", "", "path for the mined corpus")
	splitName := flags.String("split", "", "training, validation, or evaluation")
	commitsPerPath := flags.Int("commits-per-path", 50, "maximum commits inspected per candidate path")
	caseLimit := flags.Int("limit", 100, "maximum cases written")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *seedsPath == "" || *outputPath == "" || flags.NArg() != 0 || *commitsPerPath <= 0 || *caseLimit <= 0 {
		fmt.Fprintln(stderr, "benchmark mine requires --seeds PATH --out PATH --split SPLIT and positive limits")
		return 2
	}
	split, ok := parseSplit(*splitName)
	if !ok {
		fmt.Fprintln(stderr, "--split must be training, validation, or evaluation")
		return 2
	}

	var manifest benchmark.SeedManifest
	if err := decodeJSONFile(*seedsPath, &manifest); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if manifest.SchemaVersion != 1 || len(manifest.Repositories) == 0 {
		fmt.Fprintln(stderr, "seed manifest must use schema version 1 and contain repositories")
		return 2
	}
	client := dependencies.NewHistoryClient(dependencies.Getenv("GITHUB_TOKEN"))
	corpus := benchmark.Corpus{SchemaVersion: 1}
	for _, seed := range manifest.Repositories {
		cases, err := benchmark.Mine(
			context.Background(),
			client,
			seed,
			split,
			*commitsPerPath,
			*caseLimit,
		)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		corpus.Cases = append(corpus.Cases, cases...)
	}
	slices.SortFunc(corpus.Cases, func(left, right benchmark.Case) int {
		if left.MergedAt.After(right.MergedAt) {
			return -1
		}
		if left.MergedAt.Before(right.MergedAt) {
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	if len(corpus.Cases) > *caseLimit {
		corpus.Cases = corpus.Cases[:*caseLimit]
	}
	if problems := benchmark.Validate(corpus); len(problems) > 0 {
		fmt.Fprintf(stderr, "mined corpus is invalid: %s %s\n", problems[0].Field, problems[0].Message)
		return 2
	}
	if err := writeJSONFile(*outputPath, corpus); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	summary := struct {
		Cases  int    `json:"cases"`
		Output string `json:"output"`
	}{Cases: len(corpus.Cases), Output: *outputPath}
	if err := encodeJSON(stdout, summary); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

func runBenchmarkScore(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("benchmark score", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusPath := flags.String("corpus", "", "path to a corpus JSON file")
	resultsPath := flags.String("results", "", "path to analyzer results JSON")
	reviewsPath := flags.String("reviews", "", "directory containing assessment JSON files")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *corpusPath == "" || *resultsPath == "" || *reviewsPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "benchmark score requires --corpus PATH --results PATH --reviews DIR")
		return 2
	}

	var corpus benchmark.Corpus
	if err := decodeJSONFile(*corpusPath, &corpus); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var results []review.Result
	if err := decodeJSONFile(*resultsPath, &results); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	assessments, err := readAssessments(*reviewsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	report, err := review.Score(corpus, results, assessments)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := encodeJSON(stdout, report); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !report.Passed {
		return 1
	}
	return 0
}

func parseSplit(value string) (benchmark.Split, bool) {
	split := benchmark.Split(value)
	switch split {
	case benchmark.SplitTraining, benchmark.SplitValidation, benchmark.SplitEvaluation:
		return split, true
	default:
		return "", false
	}
}

func decodeJSONFile(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: multiple JSON values", path)
		}
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func readAssessments(directory string) ([]review.Assessment, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read reviews directory %s: %w", directory, err)
	}
	assessments := make([]review.Assessment, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect review file %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		var assessment review.Assessment
		path := filepath.Join(directory, entry.Name())
		if err := decodeJSONFile(path, &assessment); err != nil {
			return nil, err
		}
		assessments = append(assessments, assessment)
	}
	return assessments, nil
}

func writeJSONFile(path string, value any) (returnError error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".exposuremap-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary output in %s: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if closeError := temporary.Close(); returnError == nil && closeError != nil {
				returnError = fmt.Errorf("close temporary output: %w", closeError)
			}
		}
		if removeError := os.Remove(temporaryPath); returnError == nil && removeError != nil && !errors.Is(removeError, os.ErrNotExist) {
			returnError = fmt.Errorf("remove temporary output: %w", removeError)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set output permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace output %s: %w", path, err)
	}
	return nil
}

func encodeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	return nil
}
