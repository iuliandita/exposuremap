# ExposureMap

ExposureMap compares two revisions of container configuration and explains changes to reachability and compromise blast radius.

The project is an early prototype. It has a warning-only, bounded Docker Compose project analyzer plus a public corpus and independent-review workflow used to measure completeness, precision, and report usefulness.

## The review question

ExposureMap is designed to answer:

> Does this change expose anything new or weaken an existing boundary?

It models observed bindings as:

- `host-exposed`: published on a host interface.
- `proxy-routed`: reachable through a recognized proxy route.
- `internal`: reachable only through observed container networks.
- `unknown`: static configuration is insufficient for a verdict.

ExposureMap will not call a service Internet-accessible unless explicit project context identifies a public interface, host, or network.

## Current analyzer slice

The current CLI accepts one primary Compose file plus explicit ordered overrides for each revision. It supports bounded local includes and service inheritance, then normalizes services, resolved image references, published ports, Compose networks, bind mounts, privileged mode, and host network mode with the reference `compose-go` loader. Exact plain `${NAME}` expressions in the published-port position are preserved as symbolic bindings.

It emits two informational peer-path change kinds:

- `peer-path-added`: a host-exposed source gained a shared-network route to a distinct peer.
- `peer-path-removed`: a previously observed host-exposed-source route to a peer disappeared.

Peer changes have no severity, confidence, or remediation. They never affect the exit code or warning scorer.

The five deterministic warning rules are:

- `EM-COMPOSE-001`: new host port.
- `EM-COMPOSE-002`: loopback binding widened to a wildcard address.
- `EM-COMPOSE-003`: host network mode introduced.
- `EM-COMPOSE-004`: a host-exposed service becomes privileged or gains a sensitive bind mount.
- `EM-COMPOSE-005`: a host-exposed service gains a new shared-network path to an internal service whose exact image basename is on the conservative data-service allowlist.

Rule 005 is a medium-confidence warning based only on exact image basenames. It does not claim Internet accessibility, exploitability, missing authentication, or a vulnerability in the target image.

Run a comparison as JSON:

```sh
go run ./cmd/exposuremap diff \
  --base path/to/base/compose.yaml \
  --head path/to/head/compose.yaml \
  --format json
```

Repeat `--base-override` or `--head-override` in Compose merge order. Each revision defaults its import root to the primary file's directory. Use an explicit root when approved local imports live in a parent directory:

```sh
go run ./cmd/exposuremap diff \
  --base revisions/base/compose.yaml \
  --base-root revisions/base \
  --base-override revisions/base/compose.prod.yaml \
  --head revisions/head/compose.yaml \
  --head-root revisions/head \
  --head-override revisions/head/compose.prod.yaml \
  --format json
```

Overrides are never discovered automatically. Supported imports are short or long local `include` declarations, ordered include path lists, relative `project_directory`, and recursive same-file or local-file `extends`.

When a graph-relevant Compose variable has a known nonsecret value, pass one explicit context file:

```yaml
# .exposuremap.yml
schema_version: 1
variables:
  common:
    DATA_ROOT: ./data
  base:
    WEB_PORT: "8080"
  head:
    WEB_PORT: "8081"
```

```sh
go run ./cmd/exposuremap diff \
  --base path/to/base/compose.yaml \
  --head path/to/head/compose.yaml \
  --context .exposuremap.yml \
  --format json
```

`common` values apply to both revisions; `base` and `head` override them independently. Variable names use Compose identifier syntax and values must be YAML strings. ExposureMap never searches for this file automatically, and invalid context input is rejected without printing its path or values.

Use `--format markdown` for a review-oriented report. Complete analyses are deliberately reported as `warn`, even with no findings. Unsupported or unresolved inputs are `incomplete`. Both exit `0`; this prototype cannot become an accidental CI gate. Invalid invocations and output failures exit `2`.

Each revision may read at most 64 Compose files, 1 MiB per file, 8 MiB total, and 16 recursive import edges. Imports must be static relative regular files beneath the selected root. Absolute paths, remote references, root escapes, interpolated import paths, symlinks, non-regular files, and include `env_file` are incomplete. The isolated import view does not expose ambient `.env` files.

Label files, Dockerfiles, proxy configuration, policy, nginx, Kubernetes, Terraform, live Docker state, firewalls, DNS, Nginx Proxy Manager, and Apache remain outside the current analyzer slice. The adapter analyzes the default Compose projection with no profiles enabled, so profile-gated services are excluded. It does not infer profile settings or read process variables; interpolation uses only inline defaults and values from an explicitly selected context file. Service-level `env_file` declarations are accepted as container-environment metadata, but the referenced files are never read and their values do not affect the graph.

## What exists now

- A versioned, metadata-only benchmark corpus format.
- Twelve researched public-repository seeds with license metadata.
- Path-first GitHub history mining that avoids enumerating every pull request.
- Repository-level training, validation, and evaluation splits.
- Independent primary/secondary review with required human adjudication.
- Exact technical gates for completeness, resolution, precision, usefulness, and false CI failures.
- Immutable case materialization with upstream blob SHAs, local SHA-256 checksums, and bounded private cache files.
- A deterministic graph, informational peer-path changes, five Compose warning rules, and stable JSON/Markdown reports.
- Atomic benchmark run bundles with scorer-compatible results and per-case review reports.
- Deterministic training-derived mutations for each implemented rule.

Run the current tests:

```sh
go test -race ./...
```

Inspect the CLI:

```sh
go run ./cmd/exposuremap help
```

See [benchmark/README.md](benchmark/README.md) for the corpus and reviewer workflow.

## Safety boundary

The analyzer will never execute repository code, builds, hooks, containers, Docker, or proxy binaries. Analysis does not require network access, telemetry, or an LLM, and identical approved inputs remain deterministic.

A report with no findings means only "no warning-level boundary widening was detected within observed inputs." Informational changes may still be present, and the report is not proof that a deployment is secure. Runtime state, firewall rules, DNS, application authentication, vulnerabilities, and generated configuration may remain unobserved.

## License

Apache-2.0.
