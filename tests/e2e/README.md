# Proxyma E2E test contracts

This directory owns Docker Compose coverage for observable multi-node behavior. It is a contract suite, not evidence that every objective in the repository [README](../../README.md) is complete.

## Functional-case rule: Given / When / Then

Every functional case must follow this boundary:

- **Given** may use Docker and fixture internals to create nodes, certificates, services, topology, resource limits, partitions, crashes, or fixture readiness.
- **When** must exercise Proxyma through a public CLI command or public mTLS HTTP endpoint.
- **Then** must assert only public CLI/API status, metadata, content, or errors. Container files, Bolt buckets, CAS paths, process logs, and private transport choices are not product assertions.

Docker inspection belongs only to setup, fault injection, infrastructure checks, and diagnostics. A test may read a fixture-owned ready file, edit a pre-start config, or disconnect a container in **Given**; it may not use those internals to prove the functional **Then**. Case 05 is explicitly an infrastructure test rather than a product feature gate.

Use deadline-bounded polling from [`lib/wait.sh`](lib/wait.sh), not fixed sleeps. A terminal product error should stop a wait immediately; asynchronous success should be observed through its public result.

## Integration tests versus Docker E2E

The non-Docker contract layers and Docker layer have different ownership:

- [`cmd/proxyma-bind/integration_live_contract_test.go`](../../cmd/proxyma-bind/integration_live_contract_test.go) starts a real daemon subprocess and owns bind-to-Unix-IPC public contracts: service/storage actions, Android-facing service metadata, bandwidth/log telemetry, enrolled-peer DTOs, server streams and cancellation, enrollment, complete pipeline add/run/list/get/clone/remove lifecycle, task-status polling, and restart persistence.
- [`cmd/proxyma/cli_daemon_test.go`](../../cmd/proxyma/cli_daemon_test.go) builds the real CLI, starts a real bind daemon subprocess, and owns the CLI golden path for upload help, default and explicit upload names, storage listing, telemetry rendering, Unix IPC, and clean shutdown.
- [`internal/server/restart_contract_test.go`](../../internal/server/restart_contract_test.go) owns a real mTLS HTTP server restart contract with a temporary persistent store.
- The `androidcontract` host tag selects the Android WebRTC stubs without changing GOOS: registration is rejected and signaling returns exact JSON HTTP 501. `make test-android-contract` runs this without an emulator; `make test-android` also builds one fresh AAR and checks the required Java descriptors before Kotlin tests, lint, and assembly.
- Docker cases own behavior that requires multiple isolated nodes, Compose restarts, network faults, relay topology, cgroups, or cross-container mTLS.
- Unit/package tests own implementation details, injected failures, races, and state-machine invariants.

The live bind suite also covers the storage lifecycle (upload, subscribe/unsubscribe, sync/open, local-cache deletion, on-demand failure, and tombstone), pending-to-completed task polling plus unknown-task errors, and restart recovery of service schemas, pipeline schemas, VFS metadata, and local blob content. These remain one-process public bind contracts; they do not replace the Docker cases.

Do not duplicate a private assertion in Docker merely because an integration test already proves it. Add Docker coverage only when the container or multi-node boundary changes the observable contract.

Run the live integration contracts with:

```bash
make test-integration
go test -count=1 ./cmd/proxyma -run '^TestCLIContractLiveDaemon$'
make test-android-contract
```

## Harness entry points

[`run.sh`](run.sh) discovers `cases/*.sh`, builds the shared image once, runs selected cases with bounded parallelism, sanitizes each log, and reports every failure.

```bash
# Stable suite; this is the normal E2E green gate.
make test-e2e
make test-e2e-full

# Deterministic pull-request contracts.
make test-e2e-pr

# Network and fault-injection contracts.
make test-e2e-network

# Other named profiles.
E2E_PROFILE=functional ./tests/e2e/run.sh
E2E_PROFILE=infrastructure ./tests/e2e/run.sh
E2E_PROFILE=smoke ./tests/e2e/run.sh

# Quarantined sampler-sensitive coverage; never roll this into a green claim.
E2E_PROFILE=quarantine ./tests/e2e/run.sh

# One case, several comma-separated cases, or selection preview.
E2E_CASE=17 ./tests/e2e/run.sh
E2E_CASE=25,26,27,28 E2E_PARALLEL=1 ./tests/e2e/run.sh
E2E_PROFILE=full E2E_LIST=true ./tests/e2e/run.sh
```

`E2E_CASE` takes precedence over `E2E_PROFILE`. `E2E_PARALLEL` defaults to `3`. `E2E_SKIP_BUILD=true` reuses an already-built `proxyma-e2e-node-3` image. Running `./tests/e2e/run.sh` without a selector defaults to the stable `full` profile. `E2E_PROFILE=all` is the explicit opt-in that also includes quarantined case 13.

The helper facade [`lib/helpers.sh`](lib/helpers.sh) loads:

- [`lib/cluster.sh`](lib/cluster.sh): Compose lifecycle, node bootstrap/restart, enrollment, and public CLI/mTLS calls.
- [`lib/wait.sh`](lib/wait.sh): deadline-bounded polling with exponential backoff and terminal-failure support.
- [`lib/assert.sh`](lib/assert.sh): assertions over public output.
- [`lib/faults.sh`](lib/faults.sh): Docker-only fault injection for **Given**.
- [`lib/dump.sh`](lib/dump.sh): redacted logs and failure diagnostics.
- [`lib/case.sh`](lib/case.sh): one exit trap that captures diagnostics on failure and always performs deterministic cleanup.

Every executable case uses `set -euo pipefail`, sources the shared facade, installs `install_e2e_case_trap`, and calls `cleanup_e2e` before setup. [`scripts/validate_e2e_profiles.sh`](../../scripts/validate_e2e_profiles.sh), with its fixture contract in [`lib/validate_profiles_test.sh`](lib/validate_profiles_test.sh), checks executable bits, one unique static `E2E_PROJECT_NAME` per case, valid and unambiguous selectors, no duplicate profile entries, the stable/quarantine partition, and `pr`/`functional` parity. Run it through `make test-e2e-harness`.

Profiles are explicit selectors under [`profiles/`](profiles/):

- `functional`: deterministic public-contract cases 08, 15–19, 21, 22, 24, and 26–28.
- `pr`: currently the same deterministic set required on pull requests.
- `network`: 02, 03, 04, 07, 09–11, and 25.
- `infrastructure`: case 05 only.
- `smoke`: case 14 only.
- `quarantine`: case 13 only.
- `full`: all 26 stable executable cases; excludes quarantined 13 and the intentionally absent 23.

## Current case matrix

There are 27 executable cases: 26 stable cases in `full` and sampler-sensitive case 13 in `quarantine`. Number 23 is intentionally absent.

| ID | Public contract | Profiles / decision |
|---|---|---|
| 01 | VFS metadata, subscription-gated blob retrieval, text hash/content integrity, and OCR output propagation whose downloaded PDF is non-empty, has `%PDF-` magic, and matches its manifest SHA-256 | `full` |
| 02 | Partition isolation followed by public VFS convergence after healing | `network`, `full` |
| 03 | Relay-topology metadata and exact-content fallback download | `network`, `full` |
| 04 | Download from a surviving replica after the original source is killed | `network`, `full` |
| 05 | Container telemetry reflects configured CPU and memory cgroups | `infrastructure`, `full`; infrastructure only |
| 06 | Generic file service staging and requester VFS output registration | `full` |
| 07 | Public VFS success with loopback-only TCP and mock STUN setup | `network`, `full`; does not prove a private transport or real UPnP |
| 08 | Requester-local file auto-staging and returned output registration | `functional`, `pr`, `full` |
| 09 | Invite/join succeeds when the joiner reaches the sponsor only through a relay | `network`, `full` |
| 10 | Service failover returns the surviving provider and no-provider failure is exact | `network`, `full` |
| 11 | Live peer TLS identity rotates and post-rotation VFS traffic succeeds | `network`, `full`; needs real UDP sockets |
| 12 | Public HTTP server-stream returns at least three NDJSON messages | `full`; CLI output is informational, not the gate |
| 13 | `cheapest` selects an idle provider under synthetic load | `quarantine` only; sampler-sensitive |
| 14 | Fake JPEG frames traverse the screen stream with basic pacing | `smoke`, `full`; fake smoke only |
| 15 | A pinned three-node DAG stages input, crosses two providers, and returns exact output | `functional`, `pr`, `full` |
| 16 | CLI rejects cycle/type mismatch, accepts a valid pipeline, and persists only the valid one | `functional`, `pr`, `full` |
| 17 | A protected endpoint rejects no-cert access, invite replay fails, and the enrolled peer works | `functional`, `pr`, `full` |
| 18 | A matching service subscription affects offline validation/discovery while a non-match does not | `functional`, `pr`, `full` |
| 19 | A subscribed schema notification survives producer restart in the durable outbox | `functional`, `pr`, `full` |
| 20 | Same-basename inputs stage without collision and produce exact merged content | `full` |
| 21 | A tombstone makes replicated blobs unavailable by public open/download | `functional`, `pr`, `full`; list absence is not claimed |
| 22 | Sponsor identity, peer topology, and peer service routing survive restart | `functional`, `pr`, `full` |
| 23 | No executable case: true bidi multi-message/cancel contract is blocked | intentionally omitted |
| 24 | A subscribed download intent survives requester restart and resumes without another sync command | `functional`, `pr`, `full` |
| 25 | A stopped provider becomes publicly OFFLINE and ineligible, then returns ONLINE and runnable after restart | `network`, `full` |
| 26 | A peer preserves its VFS manifest and blob across restart, then serves exact content to another subscriber | `functional`, `pr`, `full` |
| 27 | Unsubscribe plus purge preserves remote metadata, removes cached content, and resubscribe plus public open restores the exact blob | `functional`, `pr`, `full` |
| 28 | Service removal remains absent from provider and requester discovery/run after provider restart | `functional`, `pr`, `full` |

### Why case 23 is absent

The current public HTTP/CLI streaming request accepts one JSON payload and closes client input. It cannot send multiple client messages on one bidi session, and the HTTP/CLI surface has no stream ID plus cancel command that a Docker case can drive. Bind `StreamService`/`CancelStream` cancellation is covered by the live integration tests, but that does not create a multi-message HTTP/CLI bidi contract.

Do not add a fake case that calls private handlers or treats one request plus several response chunks as bidi. Case 23 becomes valid only after a public API supports multiple client messages and observable cancellation end to end.

## README decision gates

The following objectives are not green based on this harness and must not be reported as complete:

| Objective | Current evidence gap |
|---|---|
| Dynamic port validation | “Dynamic” has no unambiguous public contract. Cases 15–16 prove DAG execution and registration-time cycle/type checks; the live integration test proves runtime input validation. Define the dynamic behavior before adding a green gate. |
| Passive background synchronization | Most cases issue `storage sync` while polling. Case 24 proves automatic recovery of one durable download intent, not a general passive metadata/blob synchronization SLO. Define the SLO and test it without manual sync triggers. |
| Real UPnP/NAT-PMP | Case 07 uses Docker networking and a mock STUN server. It neither controls a real gateway nor verifies a real mapping lifecycle. |
| OpenTelemetry load balancing | Case 13 uses host/cgroup sampling, is sampler-sensitive, and is quarantined. It does not prove OTel export or deterministic OTel-driven selection. |
| Real low-latency screen access | Case 14 uses generated JPEG frames. It does not capture/render a real screen, send input, or enforce latency/jitter percentiles. |
| `uv` Python environment and RGBA conversion | The E2E image installs distro Python/OCR packages and cases 01/06 use inline scripts. It does not exercise the claimed isolated `uv` environment or an RGBA input conversion path. |
| Visual editor, UI, and Android behavior | The Docker image exercises CLI/server processes only. TUI interaction, Compose UI, gomobile/JNI packaging, device lifecycle, and native pickers require their own non-Docker test targets. |
| mTLS for every inter-node call | Bootstrap/probing routes are intentionally anonymous (`peers/probe`, `cluster/join`, and relay forwarding policy). Case 17 proves a protected endpoint and invite security, not literal mTLS on every route. Claims must describe the intentional bootstrap exceptions. |

A passing `full` profile establishes only the contracts in the matrix. It must not be used to mark these objectives green.

## Coverage and CI verification

The local verification entry points in the repository [`Makefile`](../../Makefile) are:

```bash
make test-cover       # uncached Go coverage pass + package ratchet
make test-e2e-harness # profile invariants + validator fixtures + redaction
make test-sanitizer   # compatibility alias for test-e2e-harness
make test-ci          # test-cover + test-e2e-harness + full race pass
make coverage         # full E2E run plus unit/E2E/union reports
```

`make test-ci` intentionally performs two complete Go test passes: the coverage pass in `test-cover` and the independent `go test -race -count=1 ./...` pass. The E2E harness checks between them are shell contracts and do not run Docker E2E.

[`scripts/coverage.sh`](../../scripts/coverage.sh) defaults to the stable `full` profile, builds an instrumented [`cmd/proxyma`](../../cmd/proxyma) executable, and requires real Go `covdata`: at least one E2E coverage directory must contain both `covmeta.*` and `covcounters.*`. Missing E2E covdata fails coverage generation. `COVERAGE_ALLOW_MISSING_E2E=true` is an explicit local unit-only escape that creates an empty E2E profile; it must not be presented as E2E coverage.

The three generated reports have different meanings:

- `coverage-unit.out` is statement coverage from `go test -count=1 -coverprofile=... ./...`.
- `coverage-e2e.out` is execution coverage from the instrumented `./cmd/proxyma` binary used by Docker. Its scope is the CLI executable and instrumented code linked into it; it does not measure `cmd/proxyma-bind` or code unavailable from that executable.
- `coverage.out` is the source-block union of the unit and E2E profiles, not an average of their percentages. [`internal/covermerge`](../../internal/covermerge) validates profile modes and block metadata, combines equal source ranges (`set` uses covered-if-either; `count`/`atomic` sum), and emits each block once. [`scripts/merge_profiles.go`](../../scripts/merge_profiles.go) runs it with `--strict` so both generated profiles must exist.

The package ratchet lives at [`cmd/coverage-ratchet`](../../cmd/coverage-ratchet), with its checked-in baseline at [`scripts/coverage_baseline.json`](../../scripts/coverage_baseline.json):

```bash
go run ./cmd/coverage-ratchet check scripts/coverage_baseline.json coverage-unit.out
go run ./cmd/coverage-ratchet update scripts/coverage_baseline.json coverage-unit.out
```

`check` fails a tracked-package regression or a missing tracked package, reports an untracked package as a warning, and honors documented exclusions. `update` preserves existing exclusions and truncates measured package floors to one decimal place; the allowed drift is `0.1` percentage point.

The baseline was regenerated after the bind, CLI, and Android contracts. The repeatable floors now include bind at 75.3%, P2P at 66.9%, and server at 72.0%; the latter two use conservative repeated-run floors. The verification snapshot reported unit 66.0%, E2E 63.4%, and union 75.1%. These totals are informational—the per-package ratchet, not a transient aggregate, is the enforced contract.

CI splits responsibilities:

- The main Go job runs lint, `test-cover` plus the sanitizer, then the separate race pass, build, and CLI smoke test.
- Pull requests run both E2E `pr` and `network`.
- Pushes to `main`/`master` run E2E `full`.
- The weekly schedule runs instrumented E2E `full` plus unit/E2E/union coverage. Manual dispatch selects `pr`, `network`, or `full` and whether to collect coverage.

## Sanitized artifacts

Per-case output is written to `tests/e2e/logs/<case>.log` and sanitized before the runner reports or archives it. Failed logs are copied to `tests/e2e/logs/failed/<timestamp>/`. Cases using `dump_e2e_diagnostics` also capture sanitized Compose state, bounded logs, and public health under `tests/e2e/logs/diagnostics/`.

Sanitization removes private-key bodies and redacts bearer credentials, CLI `--token` values, JSON `token` and `secret` fields, and query tokens. It also recognizes valid V1 and structurally valid V2 invite tokens when the token occupies the complete trimmed (optionally quoted) line. It deliberately preserves ordinary SHA-256/CA hashes, Git SHAs, generic base64, and token-like substrings embedded in other text. [`lib/dump_sanitize_test.sh`](lib/dump_sanitize_test.sh), run by `make test-sanitizer`, locks these positive and negative cases.

CI sanitizes the complete logs directory again before uploading failure artifacts and retains them for five days. When a new credential format is introduced, update [`lib/dump.sh`](lib/dump.sh) and its sanitizer contract before logging it.

## Adding or changing a case

1. Choose `NN_descriptive_name.sh`; number 23 remains reserved for the blocked bidi contract.
2. Use `set -euo pipefail`, one unique static `E2E_PROJECT_NAME`, and a unique `/tmp/proxyma-e2e/...` data directory.
3. Source [`lib/helpers.sh`](lib/helpers.sh), call `install_e2e_case_trap`, and run `cleanup_e2e` before setup. The shared trap captures sanitized diagnostics on failure and always cleans up.
4. Write the scenario as **Given** fixture/topology, **When** public CLI or mTLS action, **Then** public assertion.
5. Use `wait_until`, `wait_for_output`, `wait_for_node`, or `wait_for_task_completed`; never add a fixed sleep to make timing pass.
6. Reuse helper modules instead of open-coding Compose, curl certificate flags, polling, assertions, or redaction.
7. Add the case explicitly to the appropriate profile files. Stable cases belong in `full`; deterministic public contracts may also enter `functional`/`pr`; sampler-sensitive work stays in `quarantine` with a reason.
8. Check syntax and selectors, then run the narrowest relevant contract:

```bash
bash -n tests/e2e/cases/NN_descriptive_name.sh
make test-e2e-harness
E2E_CASE=NN E2E_LIST=true ./tests/e2e/run.sh
E2E_CASE=NN E2E_PARALLEL=1 ./tests/e2e/run.sh
```

If the case changes the harness, profile ownership, or a decision gate, update this file, the testing sections in [`.cursorrules.md`](../../.cursorrules.md) and [`.agents/AGENTS.md`](../../.agents/AGENTS.md), and both testing skills.
