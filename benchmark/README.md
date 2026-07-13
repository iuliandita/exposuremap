# ExposureMap benchmark

The benchmark exists to prevent us from shipping a convincing but inaccurate configuration-security tool. It measures static completeness, finding precision, report usefulness, and CI-failure safety before hosted product work begins.

## Corpus policy

- Store repository URL, immutable base/head SHAs, pull-request number, changed relevant paths, merge time, license URL, and SPDX result.
- Do not copy third-party source into this repository.
- Fetch public source by immutable SHA into gitignored local storage when an analyzer run needs it.
- Keep every repository in exactly one dataset split.
- Keep real transitions separate from controlled mutations.
- Do not tune rules against the evaluation repositories.

`seeds.json` contains the initial public repositories and candidate paths. Its observation date is part of the file because branches, paths, licenses, and repository status can change.

## Mining

The miner uses a path-first GitHub workflow:

1. List commits touching each recognized candidate path.
2. Ask GitHub for pull requests associated with each commit.
3. Collapse duplicate commits and pull requests.
4. Union relevant paths and retain immutable base/head SHAs.
5. Sort cases deterministically and apply the requested limit.

This is substantially cheaper than listing every closed pull request in a popular repository. It fetches metadata only; source blobs are outside the mining command.

An optional `GITHUB_TOKEN` environment variable raises API limits. The token is never written to output or passed as a command argument.

Example:

```sh
go run ./cmd/exposuremap benchmark mine \
  --seeds benchmark/seeds.json \
  --out corpus.json \
  --split training \
  --commits-per-path 50 \
  --limit 100 \
  --balanced \
  --compose-only
```

`--balanced` selects cases in repository round-robin order so one active repository cannot dominate the limit. `--compose-only` retains cases with exactly one recognized Compose path, which matches the current analyzer slice. Omit it when mining future Dockerfile or proxy-parser corpora.

The output file is created atomically with mode `0600` because private-repository metadata may be supported later.

## Materialization

Materialization resolves only the paths listed in a validated corpus, at its immutable base and head SHAs. It checks Git tree modes to reject symlinks and submodules, then fetches regular-file content and verifies that the returned blob SHA matches the tree. Source stays outside the repository in a caller-selected gitignored directory.

```sh
go run ./cmd/exposuremap benchmark materialize \
  --corpus corpus.json \
  --out docs/local/cases
```

The optional `GITHUB_TOKEN` is read from the environment. It is never written to the cache or command output.

Each case is published atomically under `<out>/<case-id>/` with:

- `base/` and `head/` source trees containing only corpus-listed paths.
- `manifest.json` containing the repository, license metadata, revision SHAs, GitHub blob SHAs, byte counts, and SHA-256 checksums.
- Directories restricted to `0700` and files to `0600`.

The materializer allows at most 64 listed paths, 1 MiB per file, and 8 MiB across both revisions of a case. Existing cases are reused without network requests only after every file matches the manifest checksum and metadata. A partial or mismatched cache is an error; it is never silently accepted or overwritten.

## Validation

```sh
go run ./cmd/exposuremap benchmark validate --corpus corpus.json
```

Exit codes:

- `0`: valid corpus, successful materialization, or passing score.
- `1`: invalid corpus or failed technical gate.
- `2`: invalid invocation, unreadable input, malformed JSON, API failure, or inconsistent review data.

Validation rejects unsupported schema versions, invalid or identical SHAs, duplicate case IDs, unsafe paths, missing license metadata, mutations without an expected boundary, and repositories leaking across dataset splits.

## Evaluation runs

The batch runner verifies every cached manifest and checksum before analysis, then writes a new output directory atomically:

```sh
go run ./cmd/exposuremap benchmark run \
  --corpus corpus.json \
  --cases docs/local/cases \
  --out docs/local/evaluation/run-1
```

The bundle contains `results.json`, `summary.json`, and JSON/Markdown reports under `reports/`. `summary.json` includes total informational changes and counts by change kind. `results.json` remains warning- and scorer-only; informational changes stay in analyzer reports and never become scorer findings. Existing output directories are never overwritten. Cases with zero or multiple Compose paths are recorded as `incomplete`; cache-integrity failures stop the run.

## Controlled mutations

Generate balanced parser-to-rule mutations from complete training revisions:

```sh
go run ./cmd/exposuremap benchmark mutate \
  --corpus corpus.json \
  --cases docs/local/cases \
  --out docs/local/evaluation/mutations \
  --count-per-rule 5
```

The generated directory contains its own `corpus.json` and checksum-verifiable `cases/` cache. Mutations append isolated synthetic configuration to a real training Compose document and never execute the source. The five warning families cover a new wildcard host port, loopback-to-wildcard widening, host networking, privileged or Docker-socket access on a host-exposed service, and a new path from a host-exposed source to a recognized data service.

For a 20-per-rule `compose-v3` bundle, the 20 rule 005 mutations each emit one informational `peer-path-added` change. The other 80 controlled warnings remain peer-change-free. Informational changes do not affect warning detection, mutation recall, or proposed-failure behavior.

`--count-per-rule` must be between 1 and 100.

## Blinded review

Each meaningful report receives two independent assessments:

- `primary`: mapped locally to Codex.
- `secondary`: mapped locally to Claude.
- `adjudicator`: the human owner, required for disagreements, proposed CI failures, and sampled cases.

The role names are generic so other projects can use different reviewers. Reviewers grade factual correctness, severity, usefulness beyond the raw diff, missed changes, unsupported claims, and every emitted finding. Primary and secondary assessments use different reviewer identities and cannot read each other's work before submission.

Place one assessment JSON object per file in a review directory, then run:

```sh
go run ./cmd/exposuremap benchmark score \
  --corpus corpus.json \
  --results results.json \
  --reviews reviews/
```

## Hosted-development gate

Percentages are compared as exact integer ratios, without floating-point rounding.

- At least 100 real public-repository transitions.
- At least 100 controlled mutations.
- At least 70 percent complete graphs.
- At least 90 percent of observed Compose services and proxy routes resolved among complete scans.
- At least 95 percent warning precision.
- At least 80 percent of meaningful reports rated useful by both independent reviewers.
- Zero known false CI failures.

Mutation recall is reported but does not block until the first mutation suite establishes a defensible target. Engine and rule-pack versions are mandatory in every analyzer result so incompatible runs cannot be mixed silently.
