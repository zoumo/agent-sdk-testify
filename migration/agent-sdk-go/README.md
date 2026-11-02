# SDK-owned assertion migration

These `.pending` files are byte-for-byte originals from extraction baseline
`867a671`, imported from agent-sdk-go revision
`428263bd594bcd2586049ba38d01c5573284e0ca`. They are migration inputs, outside Go
test discovery, not skipped tests or evidence of passing SDK integration.
`manifest.json` records their original paths, byte counts and SHA-256 hashes.

Owner: **zoumo/agent-sdk-go**, restoration task: **T7** (external mock consumer).
The source SDK checkout is unchanged. T7 must port and execute these assertions
through the SDK public interfaces and external tool process before removing this
artifact:

- `TestSDKRequestUsesRunnerPeers`: structured input, remote main/probe,
  preserve/suppress, missing receipts/raw aliases and postprocessing failures.
- `TestClaudeMovedMaterialsUseCurrentSDKAndDerivedHistory`: moved material
  consumption by the real SDK, original/derived history bytes and hashes.
- `corpusSuite.TestAdoptedMaterialsRunCurrentSDKAndRetainFailures`: current SDK
  execution, retained failed-run reports, full required coverage and unchanged
  admitted recordings/index after SDK failure.

The original complete files retain imports and test context. The corpus suite's
helpers and Claude protocol/material derivation tests remain active in the tool;
only the listed SDK-dependent functions are removed from default discovery.
The active tool files now live at the repository root (`corpus_test.go`,
`materialize_claude_test.go`); protocol tests live in root `protocol/`.
Manifest `sourcePath` entries and `.pending` contents retain their historical
SDK paths and hashes. T7 must use the new root binary build entry (`go build .`).
These original tests reference old runner internals and omitted SDK fixtures /
Go driver: merely renaming them to `_test.go` here is not a valid restoration.
T7 must adapt ownership and obtain those SDK inputs from the SDK repository,
preserving every assertion and its evidence level. Do not import the SDK into
the tool, weaken matching, or count this pending transfer as SDK acceptance.

## T7 restoration

The SDK branch `codex-testify-sdk-t7` ports the three assertion groups to
`tests/simulation/simulate/{process,history}_test.go`, with additional cancellation,
unsupported-schema, missing-tool, source-corruption and remote peer cleanup checks.
See `manifest.json.restoration` for the exact assertion mapping and SDK revision.
The SDK now consumes describe/prepare/finish; its transitional fixture projections
are INTERNAL SDK data from the explicit assertion API. Runtime paths are separate
from original plan bytes. Policy runs have independent prepares/SDK hosts.

The local process checks were executed, and representative compatible builtin
materials were replayed through current public SDK APIs. Full required coverage,
source official verification and remote CI are independent gates; no release pin,
new recording or production SDK change is implied. The original source SDK main
is untouched and remains the usable legacy fallback until release cutover.

Keep these `.pending` originals and their source hashes as immutable provenance.
They are restored mappings now, not active tool tests or a claim that full corpus
acceptance passed. SDK adoption/qualification setup is intentionally not copied;
tool corpus tests retain that ownership. Final verification artifacts are retained
outside disposable worktrees by the T7 caller.
