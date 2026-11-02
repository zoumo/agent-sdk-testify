# Builtin materials and mock discovery (v1)

The installed binary embeds `builtin/materials.tar.gz` and
`builtin/manifest.json`. The current local snapshot contains 64 historical
qualified technical candidates (32/provider), selected from all 72 index entries.
Local embedding/build approval does not authorize public push or release.
Historical official SDK evidence is preserved; current SDK compatibility and new
verify diagnostics are separate results and never update these historical claims.

```sh
agent-sdk-testify mock --list --json
agent-sdk-testify mock --list --provider codex --json
agent-sdk-testify mock --describe --provider codex --case SDK-CASE-001 --json
agent-sdk-testify mock --describe --recording /captures/private-or-synthetic --json
agent-sdk-testify mock --provider codex --case SDK-CASE-001 --report /reports/direct.json
agent-sdk-testify mock --prepare --provider codex --case SDK-CASE-001 --output /reports/new-run
# run/finish use exactly the same descriptor as external materials:
agent-sdk-testify mock --run /reports/new-run/run.json
agent-sdk-testify mock --finish /reports/new-run/run.json
```

Query, prepare, run and finish are mutually exclusive modes. Case selection
requires provider and excludes `--recording`. External `--recording ABS` continues
to use the same strict engine and source hash checks. It does not acquire builtin
qualification. Neither path needs Python, official SDKs, a compiler, native Agents
or a source checkout at runtime. Direct mode requires a persistent report file;
protocol stdout remains native JSONL. Queries print JSON (also without `--json`).

The schema is [mock-catalog.schema.json](mock-catalog.schema.json). Reject unknown
`schemaVersion`; tolerate additive fields. `--list` returns every catalog/index
identity, including unavailable required and optional cases. Each row exposes
`requirement`, original `adoption`/`verification`/`verificationReason`, `available`
and `missingReason` when unavailable, the authoritative `contract`, provider
`binding` and resolved `scenario`. Describe returns one row, including unavailable
rows; starting unavailable or unknown material fails explicitly. The manifest hash
and tool version bind the snapshot. Available rows additionally expose the
original `plan` and `material`: provider/story/case/attempt, complete file hashes,
contract/binding hashes, historical official SDK identity and qualification.
`material.path` is an opaque provenance label, not a consumer filesystem API.
A 64-entry available subset is not full required coverage or SDK acceptance.

## SDK assertion inputs

For available cases, **describe** additionally returns `assertionInputs` version 1.
List omits this larger field. External describe returns the same assertion format. Consumers use this process contract rather than
opening recording/raw/report paths. The SDK still calls its own public methods;
it must not send captured frames directly to the mock or implement a matcher.

| Field | Contract |
| --- | --- |
| `schemaVersion` | `1`, independently versioning assertion input interpretation |
| `provider`, `caseId`, `attempt`, `manifestSHA256` | Stable selected builtin identity; must match the describe row/prepared builtin selection. External attempt is `external:` plus source manifest hash and has no builtin manifest hash |
| `sourceManifestSHA256`, `missingEvidence` | Original source manifest identity; explicit missing optional evidence (`sourceReport` for synthetic fixtures) |
| `connections[]` | Declared `main`, plus `probe` for remote-close; never infer a peer from disk |
| connection `format`, `version` | `agent-mock-jsonl`, `1` |
| connection `frames[]` | Original ordered envelopes with ordinal, direction, channel, epoch, elapsedMs and vendor-native `wire` JSON |
| connection `sha256`, `originalBase64` | SHA-256 and exact original JSONL bytes, including whitespace/newlines |
| `sourcePlan`, `sourceReport` | Complete original native plan and capture business report, each with id, format, sha256, originalBase64 and JSON `value` |
| `savedHistory[]` | Original frozen history/seed metadata, with opaque id, format, sha256, originalBase64, and JSON `value` or JSONL `rows` |

Frames, documents and rows are `json.RawMessage`; arbitrary native data never
passes through float64 maps. JSON output compacts whitespace only; decoded
`originalBase64` preserves exact source bytes and is the SHA-256 input. Consumers
must preserve native number lexemes and unknown fields when interpreting protected
content. Direction/order comes from the envelopes, not a new workflow language.
Original IDs, request IDs, thread/session/child IDs and all protected metadata
remain inside native values. History ids are opaque source labels: never turn them
into consumer paths. Use returned run env/workspace/materialization for live SDK
history APIs; use frozen data solely for expectations and source/hash assertions.

Native c2s values contain initialize/start/resume parameters, ordered typed inputs,
answers and control operations. Native s2c values contain expected responses,
events and identities. The complete source report preserves receipts (including
Claude `get_session_messages`), topology, child evidence/selection, facts and
protected delivery metadata without projecting away provider fields. Original
plan/configuration/seed identities remain source data, never executable paths or
authority to launch recorded programs. Model/native option incompatibility is a
consumer failure/unsupported result, never permission to change material.

External `--describe --recording ABS [--provider PROVIDER] --json` permits custom
case IDs and unadopted synthetic captures. It verifies the same mandatory
manifest-bound plan/raw, declared phase/history hashes and native recording
format as external mock. Its top-level `materialKind: external`,
`qualification: unqualified` and original `provenance` remain explicit even if a
source business report says passed. `available: true` means this external input
can be described/run; it does not mean catalog coverage or publication success.
External descriptions have no builtin `material`, catalog `contract`/`binding`
or required/optional claim. The original `plan` and assertion data are sufficient
for consumer-owned synthetic process tests. A source report, when supplied,
retains its exact JSON and a content hash; adopted report/history bytes also
retain the adoption inventory binding. Absent optional synthetic reports are
omitted and listed in `missingEvidence`; missing required plan/raw fails. No
external description is silently added to the published selection or index.

## Preparation and cleanup

Builtin prepare extracts only the selected immutable closure inside the run-owned
`resourceRoot/builtin`. The descriptor adds `builtin` with `manifestSHA256` and
`material`; protocol receipts retain this selection. Run validates the entire
selected closure, including official evidence, before starting the existing
strict peer. Finish validates closure and all connection receipts, then removes
the owned root even after protocol/hash failure. Reports, plan contract and
history materialization remain in persistent output. Repeated finish uses saved
cleanup/material-validation evidence after removal and cannot erase a failure.
Direct mock removes its selected extraction on success/failure. See
[mock-run.md](mock-run.md) for the shared lifecycle, locks and parent env contract.

## Reproducible offline material build

After explicitly authorized record → official verify → existing qualification and
atomic adoption, generate from the current authoritative catalog/index/adoption.
This command does not record, run SDKs, scan, redact or change source materials:

```sh
mise exec -- go run -tags builtin_generate . --revision FULL_40_CHARACTER_SOURCE_COMMIT
mise exec -- go run -tags builtin_generate . --check
bash scripts/build-builtin.sh /absolute/output/agent-sdk-testify
```

The build-only tag runs the internal generator, not an installed fourth command.
Use the full source revision containing the selected catalog/materials. Check
reuses the manifest source revision so later implementation commits do not create
a self-referential revision/hash cycle. A new material or contract snapshot needs
explicit regeneration at its actual source revision and must travel with the tool
version. `protocol/command.Version` is also bound and checked; normal version
changes require regeneration. `scripts/simulation.sh build` uses the checked build
before building the official wheel. Plain `go build` embeds the committed snapshot;
use the checked build for corpus freshness. Default tests also check freshness.

Generation validates the full index with `loadIndex`, selects exactly admitted +
verification passed, and reuses `verifyAdopted` and existing admission/content
rules. Known failed/unverified/unsupported vetoes remain in coverage. Every
selected adoption/review/source/official-evidence file is hash-bound and copied
unchanged. Original inventories/official receipts remain inside the closure;
new manifest bindings never mutate their historical identities. Tar members and
manifest keys are sorted, tar/gzip timestamps fixed, with no caller path/time.
Missing/drifted closure, malformed identities, stale generated outputs or an
empty selection fail the check/build. New SDK compatibility diagnostics do not
select candidates or replace historical evidence.

Consumers read describe before prepare, retain assertionInputs/manifest identity,
apply the returned env/workspace to the SDK host, use each declared command or
launcher, close/wait, then always finish. A prepared launcher's sole `--version` (or `-v`)
probe returns recorded native CLI compatibility metadata from contract `cliVersion`
(or an explicit unknown/mock response), without consuming a connection or writing
success receipts. It is separate from the installed `toolVersion`; see
[mock-run.md](mock-run.md). Prepare independently for policy
comparisons. SDK business assertions, current compatibility and full coverage
remain SDK-owned. Local binaries are suitable for integration; no published pin
or public release is implied.

The imported tracked materials originate at public `zoumo/agent-sdk-go` revision
`428263bd594bcd2586049ba38d01c5573284e0ca` (tree
`740858be23f1d950030198406da24823c4f03fc0`). Keep the repository license and
`docs/import-baseline.json` provenance with distributions. This existing source
publication introduces no additional manual-review or scanning gate for local
embedding; actual push/release remains a separate action.

Authoritative source paths are now root `scenarios/`, `recordings/`, and `drivers/`.
The historical import manifest retains original paths; `import-current-paths.json`
resolves relocated paths without changing historical hashes.
