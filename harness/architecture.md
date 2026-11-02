---
type: Architecture
title: SDK 架构与依赖边界
status: stable
---

# Architecture standards

These rules describe the agreed repository boundaries. For document ownership and
navigation, see the [harness index](index.md).
The provider-independent behavior contract and onboarding requirements live in
[SDK behavior contract](sdk/architecture.md); this page governs repository module
and dependency boundaries.

- Use one module, `github.com/zoumo/agent-sdk-go`, and one version. Keep a single
  root `go.mod`/`go.sum`; do not introduce nested modules or `go.work`.
- Keep the supported native SDK imports `codex`, `codex/types`, `claude` and
  `claude/types` independent. Do not add a root generic client, old-path forwarding
  packages or a shared native answer state machine. Common approval operations
  follow the [SDK behavior contract](sdk/architecture.md), preserving original
  native answer handles and provider-specific options.
- `codex` and `claude` must not import each other. Native SDK production code
  must not depend on recording/replay tools or fixture containers.
- Both native `types` packages must have standard-library-only **production**
  dependencies. Their tests may use testify. Dependency gates must distinguish
  production dependencies from test dependencies, and remain enabled.
- Shared runtime code must not import either native SDK or native `types`.
  `internal/lineage` accepts confirmed relationship facts; it does not parse
  native events, infer creation, send protocol messages or grant answer authority.
  Its precise contract and resource bounds live in [lineage](modules/native-session/design.md#lineage-shared-relationship-rules).
- `internal/queue` shares only concurrent FIFO mechanics. It imports no native
  SDK, native types, tools or fixtures. Its only non-standard production dependency
  is the pinned `github.com/zoumo/golib/channx.ChannX` implementation; other golib
  packages must not enter SDK production dependency trees. Native types remain
  standard-library-only.
  FIFO ownership and resource semantics live in [event delivery](modules/native-session/design.md#delivery-event-delivery).
- Native parsing, unknown data, aliases, execution identities, transport and
  process lifecycle stay with each native SDK. Session state ownership and
  internal coordination live in the [Session module design](modules/native-session/design.md).
- Answers require their original native request/permission handle. Relationship
  records carry context only. Do not restore `TreeRecord`/`TreeEvent` APIs.

Testing and source placement follow the [testing standards](testing.md) and
[code layout](#layout-code-layout-standards).

<a id="layout-code-layout-standards"></a>

## Code layout standards

Both native SDKs use the same separation of responsibilities:

| Location | Responsibility |
|---|---|
| `codex/`, `claude/` | Independent native SDK runtime and native types |
| `tools/agent-sdk-testify/protocol/` | Generic Go stdio recording proxy, protocol mock, command and tool-owned tests; uses the root Go module |
| `tools/agent-sdk-testify/` | Single executable and Go environment/process runner; single-request native drivers own case execution and assertions |
| `tests/simulation/drivers/` | Go simulation process entry, Python packaging and driver documentation |
| `tests/simulation/drivers/codex/official/`, `tests/simulation/drivers/claude/official/` | Agent-owned official SDK drivers |
| `tests/simulation/drivers/codex/go/`, `tests/simulation/drivers/claude/go/` | Agent-owned Go SDK simulation flows, history, child and remote assertions and regression tests |
| `tests/simulation/drivers/common/`, `tests/simulation/drivers/tests/` | Shared official material readers, native helpers and offline driver/process tests |
| `tests/simulation/scenarios/` | Shared SDK story/case materials and provider bindings, supplied explicitly to either driver |
| `codex/tests/replay`, `claude/tests/replay` | Synthetic fixture decoding/process adapters and SDK integration tests; SDK exercise helpers remain test-only; protocol matching belongs to agent-sdk-testify |
| `internal/mocktest` | Shared standard-library test process bridge, without native matching or SDK imports |
| `codex/tests/testdata/integration`, `claude/tests/testdata/integration` | Explicitly synthetic protocol inputs owned by code integration tests |
| `codex/tests/e2e`, `claude/tests/e2e` | Explicitly enabled real CLI/agent checks; scope and evidence are documented separately from offline tests |
| Each native `examples` | Genuine SDK usage and offline usage demonstrations |
| Each native `*_test.go` | Public API checks and SDK integration against mock CLI processes |
| `tests/integration` | Shared code integration suite, native test adapters and synthetic protocol peers |

SDK mock integration tests launch `agent-sdk-testify protocol mock` through the native public process
options. Mock commands are tested through their public process interface. External fetched-module checks
retain public import/configuration and fixture packaging smoke tests, plus public
example and documentation compilation; they do not duplicate SDK interaction suites.

`tools/agent-sdk-testify/` is the tool executable; `tests/simulation/drivers/` is a
separate Go SDK acceptance driver executable. The tool must not import that driver
or the production SDKs. Top-level
`record`, `replay` and `simulate` commands own case orchestration; `protocol record`,
`protocol mock` and `protocol connect` expose the independently usable protocol module.
The runner uses its own executable by default; callers need no separate peer binary.
`protocol/` and its `capture`, `command`, `rawjson`, `recording` and `replay` packages
own protocol processing and remain independent of case orchestration, drivers and SDKs.
The capture core owns its options, I/O and typed errors; only the command boundary
maps those errors to CLI exit codes. Provider-specific native preparation and
history-seed interpretation stay in provider files, outside generic process orchestration.
Do not add `cmd`, `pkg` or forwarding layers. Module responsibilities and dependency
gates follow the module boundaries above.

Each native `tests/testdata/embed.go` is a small standard-library-only fixture
container because `go:embed` cannot read a parent directory. Embedded paths
resolve within the container, independently of the caller’s working directory.
SDK tests and offline examples reuse fixture access; example-local synthetic data
may remain with its example. The mock tool owns protocol matching; fixture adapters
only decode data and bridge processes. SDK integration tests assert SDK results;
simulation operation sequences and business assertions live with the provider driver
under `tests/simulation/drivers/<agent>/go/`. Test-local SDK policies belong in `*_test.go`, not
exported runtime helper packages. Pure protocol matching matrices live with the
protocol module, without duplicate copies in SDK test adapters.

Move data rather than copying it. Update imports, embeds, scripts, examples, CI
and documentation links whenever paths change. Recording inventories retain
direct asset references and evidence limits, and link back to these layout rules.

<a id="layout-simulation-data-and-integration-fixtures"></a>

### Simulation data and integration fixtures

Simulation attempts are complete private datasets outside tracked Git content. Reviewed regression materials live under `tests/simulation/recordings/<provider>/<story-id>/<case-id>/<attempt>/`; `tests/simulation/recordings/index.json` owns the fixed required coverage and adoption references. The corpus CLI preserves private originals separately from the reviewed subset.
Source identity, observed versions, raw hashes, sanitization and publication
review follow the [recording data contract](modules/agent-sdk-testify/design.md#story-数据格式与存储).
Preserve original bytes and provenance when moving data; synthetic data must never
be labeled real.

Each native `tests/testdata/integration` contains synthetic protocol inputs for
code integration tests, with semantic names rather than simulation case identities.
Test-only peers and fault controls stay in test code, without a separate
technical-check registry. Story/case scope follows the [case catalog](sdk/validation-cases.md).

agent-sdk-testify 的通用协议入口读取显式提供的录制或合成输入；内置场景入口从随版本
发布的合格材料中选择并准备资源。发布资格、环境和下游边界见
[工具设计](modules/agent-sdk-testify/design.md#内置材料与下游直接使用)。
工具专项测试不构成已发布的真实录制仿真证据。

## 编码约定

使用清楚的名称和语言惯用写法；注释解释原因，避免重复代码。按实际职责划分包，
沿用现有 SDK 平铺布局，不新增无需求的 cmd/pkg、框架或转发层。
复用须解决实际重复，性能优化须有实际需求。第三方依赖应有明确用途、版本与来源。
遵守已有项目配置，限制 lint 例外并说明原因；lint 不替代编译、测试和 race。
日志与协议 stdout 分离，不记录认证秘密；可复用模块不修改全局日志或遥测配置。
