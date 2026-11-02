# Prepared mock contract (schemaVersion 1)

Prepared mock is an installed CLI/process interface. It needs the tool binary and
builtin provider/case selection or an absolute external recording directory; it needs no official SDK, native Agent,
Go compiler or source checkout. It never runs captured commands or model calls.
The caller owns SDK operations, assertions, remote services and policy comparisons.

```sh
agent-sdk-testify mock --prepare --recording /captures/attempt --output /reports/new-run
agent-sdk-testify mock --run /reports/new-run/run.json
agent-sdk-testify mock --run /reports/new-run/run.json --connection probe
agent-sdk-testify mock --finish /reports/new-run/run.json
# Equivalent finish spelling:
agent-sdk-testify mock --finish --run /reports/new-run/run.json
```

`--output` must be a new absolute directory, with an existing parent, outside the
recording. It is persistent: finish never deletes it. Prepare prints the complete
JSON descriptor and writes the same object to `run.json`. Run stdout is exclusively
native stdio JSONL; finish prints JSON and writes `finish.json`. Diagnostics use
stderr. A nonzero exit always fails the operation; inspect the persistent reports.
Direct `mock --recording ABS --report FILE` remains supported.

## Descriptor

All paths are absolute. Treat this data as read-only; do not source/eval it.
Consumers must reject unsupported schema versions and may ignore new fields.

| Field | Meaning |
| --- | --- |
| `schemaVersion` | Integer `1` |
| `runId` | Random identity unique to this prepared execution |
| `tool.command`, `toolVersion`, `toolSHA256` | Preparing executable and its actual identity, separate from recording provenance |
| `recording`, `provider`, `caseId` | Selected immutable source directory, provider and optional case identity |
| `resourceRoot` | Tool-owned temporary root, with a matching `.owner` run identity |
| `workspace` | Existing writable fixture project; initial files only, not replayed tool side effects |
| `protocolCwd` | Original Codex project identity; Claude uses the new workspace. An absent original Codex cwd is an empty string |
| `env` | String-to-string environment overrides for **the SDK host**, applied before host startup; also applied to peers |
| `contract`, `contractSHA256` | Persistent original plan bytes (including scenario, inputs, expectations, native args, model and original identities) and SHA-256 |
| `materialization`, `materializationSHA256` | Persistent history copy/derivation index and SHA-256 |
| `connections` | All selected protocol connections; each must complete independently |
| `finish.command`, `finish.args` | Exact cleanup/check command, callable again |

`env` supplies isolated HOME, TMPDIR/TMP/TEMP and XDG paths. Codex receives
CODEX_HOME; Claude receives CLAUDE_CONFIG_DIR and its history-event/updater settings.
The config contains only the recorded model; no user auth, hooks, skills or plugins
are copied. Saved history is copied through the existing provider preparation.
Only manifest-bound saved history or plan-bound history seeds are accepted.

The consumer launches its SDK host with `workspace` as process cwd and merges
`env` into the host environment. Configure Codex's protocol project/cwd using
`protocolCwd` and the immutable contract, independently from the writable workspace.
Configure Claude's WorkDir/history queries using `workspace`. Setting only the
mock child's environment does not configure a parent's history API.

A connection contains:

| Field | Meaning |
| --- | --- |
| `id` | `main`, plus `probe` only for a `remote-close` contract |
| `command`, `args` | Exact installed CLI invocation; default `--run` selects main |
| `launcher` | Temporary executable adapter for SDKs accepting only an executable path; invoke for protocol startup or with sole `--version` (or `-v`) for a metadata probe |
| `recording` | Parent capture directory (also for probe) |
| `planSHA256`, `manifestSHA256`, `recordingSHA256` | Selected source plan/manifest and this connection's raw stream identity |
| `receipt`, `processReceipt` | Independent persistent protocol and supervisor exit reports |

Each allocation is single-use. Repeated or concurrent starts of the same
connection fail; independent main/probe processes may run concurrently. Undeclared
connections fail. Consumers passing native startup flags to `command/args` receive
the same strict transport/argument checks as direct mock. No flags relax matching.
The launcher deliberately supplies only its declared command for protocol startup.
A sole `--version` (or `-v`) instead invokes `mock --run FILE --connection ID --version`.
This read-only probe returns the immutable contract's `cliVersion`: the recorded
native CLI version used as SDK compatibility metadata, not the installed tool or
SDK identity. Missing/empty recorded versions return
`unknown (mock; recorded native CLI version unavailable)`. The actual tool's
`--version` and descriptor `toolVersion` remain the installed tool identity.

Metadata probes validate the descriptor, declared connection, ownership and
contract hash. They do not create locks, reserve/start the protocol connection or
write protocol/process receipts. Repeated probes leave the same connection
available for its single protocol execution. Probe-only runs still fail finish:
all full-consumption, EOF and process-exit evidence remains required. SDK startup
arguments otherwise retain the launcher's existing declared-command behavior;
command-prefix native startup arguments retain strict validation.

## History and reports

Original plan/raw files remain unchanged. `materialization.json` keeps `copies`
with source/derived paths and both SHA-256 values. Claude additionally retains its
existing `transformations` entries: only top-level cwd and project-directory keys
change; IDs, nested cwd, unknown fields and number lexemes retain their identity.
Source copies remain alongside derived copies until cleanup. Finish checks source
and derived hashes before removing temporary data. A failed hash check survives
cleanup and subsequent finish calls.

`<id>.receipt.json` retains the direct mock report: status, provider, plan/raw
hashes, provenance, fullConsumption, clientEOF, nativeLaunched, cleanupComplete,
exitCode and optional error. Prepared receipts also bind `runId` and `connectionId`.
The supervisor waits for the protocol child and atomically commits
`<id>.process.json` with schemaVersion, runId, connectionId, status, actual exitCode,
actual tool path/version/hash and optional error. Missing or killed supervisors
cannot substitute protocol success for exit evidence.

After closing its Session/Runtime and waiting for all peer processes, the consumer
calls finish even if its own business assertions failed. Finish verifies **every**
selected connection's identity, strict success, full consumption, EOF, no native
launch, cleanup and normal exit. `finish.json` contains schemaVersion, runId, status,
per-connection status/report paths/errors, history status, cleanupComplete and any
error. Success here proves protocol/environment completion, not SDK business success.

Finish refuses cleanup while a peer holds the run lock; it persists that failure
and can be retried after closing/waiting for peers. The protocol child inherits
the lock, including if its supervisor is killed. Cancellation forwards TERM then
bounded KILL to owned process groups. A canceled or abnormal execution remains
failed. Finish never kills arbitrary consumer processes: the consumer must first
close its own host/services and cease using the directories.

Once idle, finish marks the run closed and attempts cleanup even when receipts or
hashes fail. It deletes only the recorded owned temp root after checking its owner
marker. Ownership/removal failures leave cleanupComplete false and are retryable.
`cleanup.json` retains cleanup/history evidence for repeated finish. `run.json`,
contract, materialization, protocol/process receipts and diagnostics remain in
output. `prepare.json` retains preparation failure and partial cleanup results;
existing outputs and consumer files are preserved. A failed protocol run cannot
become successful merely because cleanup succeeded.

## T5 and T7 integration

Builtin selection and versioned describe assertion inputs are documented in
[builtin.md](builtin.md) and [mock-catalog.schema.json](mock-catalog.schema.json).
`--prepare --provider PROVIDER --case CASE` uses this same lifecycle. Its optional
`builtin` descriptor field binds the manifest and selected material. Extracted
materials live under the owned root and are removed by finish, while immutable
contract/materialization and receipts remain persistent. Consumer assertion data
comes from describe; `recording` is not a filesystem interface for SDK assertions.

T7 should parse this schema, start its own SDK runner with the returned cwd/env,
consume the selected command or launcher, close/wait, and always run finish.
For Go SDK preserve/suppress comparisons, call prepare **twice**, with distinct
outputs and independent environments/receipts. The SDK owns that comparison and
any WebSocket bridge. This tool declares only case-required main/probe connections;
it does not add SDK suppress workflows. Persist SDK assertions alongside the two
protocol reports and require both business and finish success.
