# 消费者录制

外部消费者可以使用 `prepare --plan` 创建会话，再由 record 把自身 SDK 或运行时发往
真实 Agent 的原生交互保存为 consumer material，并在同一会话中直接用 mock 严格
回放。该路径不经过官方 driver，不登记官方 case，也不进入验证、采用或内置快照。

consumer 录制采用 `prepare → record → mock → finish` 生命周期。一次
prepare 可以承载多个 record、verify 或 mock execution；环境准备和材料由工具完整负责，
消费者只提供 plan、真实 Agent 启动命令/参数，并在自己的 SDK 宿主演用当前 route 的
环境。本文只描述 plan source；生命周期、source/executor/target 和 descriptor 合同见
[工具设计](design.md#prepared-session-生命周期)。

## 边界

- 工具负责隔离根、workspace、HOME、配置发现与筛选、tmp、历史种子、材料写入和清理。
- 消费者负责 plan、真实 Agent executable 和启动参数、SDK 宿主、业务断言和收尾调用。
- 消费者不能提供或覆盖隔离根、workspace、HOME、config、tmp 或原生环境变量。
- 工具不启动消费者的 SDK 宿主，也不生成客户端请求；客户端始终是消费者自己的 SDK。
- 材料是 external/consumer 材料，只能按路径消费，不参与官方资格、采用和发布。
- consumer material 不能证明官方 SDK 兼容、业务断言通过、工具副作用发生或覆盖完整。
- v1 只支持一条 `main` 连接；`probe` 和其他多连接流程不在本版本范围内。

## 生命周期

### prepare

```sh
agent-sdk-testify prepare --agent codex \
  --plan /absolute/consumer-plan.json \
  --session-dir /absolute/session \
  [--history-seed /absolute/seed] \
  [--protocol-bytes N] [--stderr-bytes N] \
  [--max-frames N] [--max-frame-bytes N]
```

`prepare` 不启动真实 Agent。它创建持久输出目录和工具拥有的临时根，准备：

- `resourceRoot`、`workspace`、`home`、`config`、`tmp`；
- Codex 的 `CODEX_HOME` 或 Claude 的 `CLAUDE_CONFIG_DIR`；
- 从 agent type 标准原生配置位置发现并筛选的原生配置和认证副本；
- plan 的 `model` 配置和 `scenario.files`；
- 可选 `--history-seed` 历史种子；
- 会话目录内的 `session.json`、record/mock route 的环境槽、连接模板和 `finish` 命令。

`--plan` 来源不接受配置目录参数。record 的 `--cmd` 必须是绝对路径；`--` 后逐项
提供 Agent 启动参数，不经过 shell，仍须通过回放白名单校验。
CLI 和原生参数属于本次 record execution，不改变已准备的 source、workspace、HOME、config
或历史。

### 配置发现

`prepare` 按 agent type 解析只读配置来源：

- Codex：调用进程已设置且为绝对路径的 `CODEX_HOME`，否则调用进程的 `$HOME/.codex`。
- Claude：调用进程已设置且为绝对路径的 `CLAUDE_CONFIG_DIR`，否则调用进程的
  `$HOME/.claude`。

非绝对路径、符号链接、不可读目录或格式错误的配置使 `prepare` 失败。来源不存在时，
工具创建空隔离配置；它不推断其他配置目录，不执行登录，也不生成认证。真实 Agent 仍
可使用当前进程中白名单内的 agent 认证环境变量。

工具只复制并筛选以下原生文件：

- Codex：`config.toml`、`auth.json`，保留选中 agent 的设置和认证引用。
- Claude：`settings.json`、`.credentials.json`，只保留模型设置和白名单认证环境。

个人 skills、插件、记忆、项目指令、hooks、MCP 配置、agents 和无关会话不会进入隔离
环境。目标 `config` 目录以及 `CODEX_HOME`/`CLAUDE_CONFIG_DIR` 由工具决定；
`resourceRoot`、`workspace`、`home` 和 `tmp` 也是工具生成的路径。消费者不能通过
flag 覆盖，只能使用 descriptor 返回值。

### record

```sh
agent-sdk-testify record --session-dir /absolute/session \
  [--timeout DURATION] \
  --cmd /absolute/codex -- app-server --listen stdio://
```

消费者必须先应用 record route 的 `env`，把 SDK 宿主 cwd 设为 `workspace`，再把
自己的 Agent executable 指向 route 的 `connection` 命令或 launcher。record 从环境
基线创建独立 execution overlay，在已准备好的环境中启动真实 Agent，转发 stdio，并写入
原始帧和传输报告。每个连接只能启动一次；失败也保留原始证据。

record 完成后把 capture 登记到会话，更新最新兼容 artifact；随后可以直接在同一会话
执行 `mock`，不需要重新 prepare。consumer material 永远不能进入 `verify`。

### mock

```sh
agent-sdk-testify mock --session-dir /absolute/session \
  --connection main
```

mock 默认消费会话中最新的 record capture，使用新的 execution overlay 和预先刷新的回放
环境。消费者按 mock route 应用 `env`、cwd 和 `connection`，关闭 SDK 后继续 finish。

### finish

```sh
agent-sdk-testify finish --session-dir /absolute/session
```

消费者先关闭自己的 SDK、Session 和 Runtime，等所有 peer 退出，再调用 `finish`。
`finish` 逐 execution 校验 record 和 mock、重新核验历史状态、manifest/report 和哈希，
然后删除工具拥有的临时根。工具不会结束消费者的进程，也不会删除会话目录。
finish 失败可重试；清理成功后再次 finish 使用持久 cleanup 证据，不重新读取临时根。

## 多次使用

- record 完成后，mock 默认使用最新 capture；可在同一会话直接执行。
- 多次 record 产生多个 capture，新 capture 成为默认选择，旧 capture 仍可按显式
  `--recording PATH` 使用。
- 每次 record 或 mock 使用独立 execution overlay，前一次 SDK 的历史和写入不会污染后一次。
- 同一会话的 action 串行执行；上一个 action 完成后才可以开始下一个。
- consumer capture 不进入 verify；需要官方验证时必须重新走官方 case 录制。

## 环境职责

| 事项 | 工具负责 | 消费者负责 |
| --- | --- | --- |
| 隔离根、workspace、HOME、tmp | 创建、隔离、清理 | 使用 descriptor 路径 |
| 原生配置、认证副本 | 按 agent type 标准位置发现、筛选并复制 | 使用 descriptor，不提供配置路径 |
| model、fixtures、历史种子 | 写入隔离环境 | 在 plan/参数中声明 |
| 真实 Agent executable/args | 校验、启动 | 提供绝对路径和受支持参数 |
| SDK 宿主环境 | 生成 `env` descriptor | 在启动 SDK 前应用 |
| 协议请求和响应 | 原样代理、录制、严格回放 | 发送请求、接收响应 |
| 业务断言和产品 E2E | 不参与 | 执行并报告 |

## Consumer plan

`plan.json` 是消费者提供的运行合同，原字节复制到材料，并由 manifest 的
`planSHA256` 绑定。v1 使用以下字段：

| 字段 | 要求 | 用途 |
| --- | --- | --- |
| `version` | 必须为 `1` | plan 格式版本 |
| `agentType` | `codex` 或 `claude` | 必须与 `--agent` 或自动推断值一致 |
| `caseId` | 非空字符串 | 消费者自定义身份；不是官方 catalog case |
| `driverProfile` | 必须为 `consumer` | 禁止官方 remote-close 和 probe 流程 |
| `project` | 绝对路径 | 原始协议 cwd；不是回放时的新 workspace |
| `model` | 可选字符串 | 写入隔离环境的模型名 |
| `cliVersion` | 可选字符串 | launcher 回答 `--version` 时的原生 CLI 版本 |
| `scenario.files` | 可选对象 | 相对路径到文本内容的可写 fixture |
| `scenario.parameters` | 可选对象 | 消费者自定义参数；工具不解释 |

最小示例：

```json
{
  "version": 1,
  "agentType": "codex",
  "caseId": "consumer-two-turns",
  "driverProfile": "consumer",
  "project": "/absolute/original-project",
  "model": "model-name",
  "cliVersion": "native-cli-version",
  "scenario": {"files": {"fixture.txt": "fixture content\n"}}
}
```

未知字段必须保留。`project` 只表示协议项目身份；工具创建新的 workspace，消费者不能
把回放或录制 workspace 写进 plan 冒充原始 cwd。

`scenario.files` 只支持文本内容。二进制 fixture、认证文件和个人配置不属于 v1
consumer plan；需要历史的消费者使用 `--history-seed`。

## Session 与 execution descriptor

`session.json` 是 prepare 在会话目录内创建的持久、只读合同，至少包含：

| 字段 | 用途 |
| --- | --- |
| `schemaVersion` | 当前为 `1`；不支持的版本必须拒绝 |
| `sessionId` | 本次会话的唯一身份 |
| `tool`, `toolVersion`, `toolSHA256` | 实际执行的工具及身份 |
| `agentType`, `source`, `sourceSHA256` | 本次会话的来源身份 |
| `historySeed` | 可选的规范化历史种子来源路径 |
| `resourceRoot` | 工具拥有的临时根 |
| `workspace` | SDK 宿主 cwd 和可写 fixture |
| `protocolCwd` | Codex 原始项目 cwd；Claude 使用 workspace |
| `routes` | record、mock 等 route 的稳定环境、连接模板和证据策略 |
| `finish` | 会话收尾命令 |

每次 record 或 mock 另在 `executions/<executionId>/execution.json` 写入本次
execution 的只读合同，包含 `executionId`、`sessionId`、`command`、`args`、
source artifact、workspace overlay、`env`、
`connections`、receipts、artifactRoot 和 limits。消费者不得 source、eval 或改写
`session.json` 或 `execution.json`。当前 route 的 `env` 必须应用到自己的 SDK 宿主，
不能只设置录制或 mock 子进程。execution 创建后不得重新生成或覆盖。

finish 删除工具临时根后，其中 `resourceRoot` 等路径失效；会话目录里的材料仍可按
`mock --recording` 消费，但不能再次启动已经结束的 execution。

如果消费者的 SDK 会向 Agent executable 追加原生参数，应使用 `connection.launcher`；
launcher 把受支持的追加参数转发给真实 Agent，并拒绝重复或白名单外的参数。

## 原生启动参数

v1 复用 `mock` 的原生启动参数白名单，避免录出一份无法回放的材料：

- Codex：`app-server`、`--listen stdio://`。
- Claude：`--output-format stream-json`、`--input-format stream-json`、
  `--permission-prompt-tool stdio`，可选 `--verbose`、
  `--include-partial-messages`、`--model=...`、`--resume=...`。

重复参数、未知参数和空格形式的 `--model VALUE`/`--resume VALUE` 在 record 启动前失败。
需要其他原生启动参数时，先扩展回放 adapter 和白名单；不能只放宽录制侧。

## 材料布局

record execution 完成后生成可由同一会话 mock 或 `mock --recording` 直接消费的目录：

```text
<session>/executions/<executionId>/
  execution.json
  record/
    plan.json
    manifest.json
    report.json
    raw/raw.jsonl
    raw/manifest.json
    raw/transport.json
    raw/stderr.raw
    config/projects/...      # record 完成时存在
    config/sessions/...      # record 完成时存在
    history-seed/...         # record 完成时存在
```

- `record/` 是可交给同一会话 mock 或 `mock --recording` 的材料根；同目录上层的
  `execution.json` 是本次 record 的只读审计 descriptor。
- `raw/raw.jsonl` 使用 v1 stdio 帧格式，保留 ordinal、direction、channel、epoch、
  elapsedMs 和原始 wire JSON。
- `manifest.json` 至少包含 `planSHA256`、`runSHA256`、`files["raw.jsonl"]`、
  `recordingFormat`、`proxyVersion`、`native`、`captureStatus` 和
  `provenance: "consumer"`。`files` 中的路径相对 `raw/`，与既有材料布局一致。
- `raw/manifest.json` 是采集层 manifest；`raw/transport.json` 保留传输报告；
  `raw/stderr.raw` 是原生 stderr。
- `report.json` 记录采集状态、客户端 EOF、原生启动、清理和失败原因。
- `manifest.savedFiles` 绑定 record 完成时的历史文件及 SHA-256；finish 只重新核验。
- 不生成 `adoption.json`、`review.json`、`qualification.json` 或
  `official-replay/`。

## 状态与历史

`--history-seed` 是可选的完整历史种子，由工具校验后复制到隔离环境。record 完成时只快照
隔离环境中的以下子树：

```text
config/projects/...
config/sessions/...
history-seed/...
```

工具只写入普通文件，拒绝符号链接、目录穿越和其他路径。快照完成后，每个文件的
SHA-256 写入 `manifest.savedFiles`。`plan.json` 不被改写，`planSHA256` 仍绑定
消费者原始 plan。

回放时，mock route 从材料复制这些文件到本次 execution overlay；finish 校验种子文件
未被改写。SDK 在回放期间可以创建新的会话或历史文件，但不能修改已绑定的种子文件。
没有历史种子的录制从空状态开始，只覆盖本次协议交互。

## 使用 mock

```sh
agent-sdk-testify mock --describe --recording /absolute/material --json
agent-sdk-testify prepare \
  --recording /absolute/material --session-dir /absolute/session
agent-sdk-testify mock --session-dir /absolute/session
agent-sdk-testify finish --session-dir /absolute/session
```

`mock --describe` 返回 `materialKind: external`、`qualification: unqualified` 和
`provenance: consumer`，并提供 `assertionInputs`。consumer material 不进入
`mock --list`，也不能通过 `--agent/--case` 选择；它只能按 `--recording` 路径消费。

消费者仍负责 SDK 调用、业务断言、策略比较和真实 E2E。`verify` 只接受官方 driver
和官方材料，不用于 consumer material。

## 可重复性

consumer recording 是字节级录制，不是语义模拟。回放要求消费者再次发送同样的客户端
帧；只有 Codex JSON-RPC ID 和 Claude control request ID 按既有 adapter 规则重绑定。
工具不提供 UUID、时间戳、cwd、业务 ID 或任意字段的通配匹配，也不会自动搜索其他
录制或回退到真实 Agent。

如果消费者每次运行生成新的 UUID、session 标记或其他输入字段，必须先提供确定性的
测试模式，或改用消费者自己的 stateful peer。重新录制一次不能解决运行间差异。
consumer material 至少应连续回放两次并保持严格匹配，才能作为该消费者的回归基线。

## 证据与发布

plan source 的 record 只报告本次采集的完整性：真实 Agent 已启动、客户端 EOF、帧和
哈希完整、进程退出和工具自有资源清理成功。它不报告官方 SDK 业务结果、产品 E2E
通过或覆盖完整。

consumer material 归消费者保存和发布；本仓库不写入 `data/index.json`，不生成内置
快照。需要官方采用时，必须用 `prepare --case` 和 record 重新采集，再走 `verify`、
资格审核和采用，不能把 consumer material 提升为官方材料。

## 安全与失败

- 输出目录和工具临时根使用私有权限；`stderr.raw` 可能包含提示词、认证错误或环境
  信息，材料默认保持私有，不进入公开归档。
- 只从 agent type 标准配置位置复制允许的原生配置和认证；不读取任意配置路径，不复制
  个人配置、项目指令或无关会话。
- prepare、record、mock 或 finish 失败都保留失败证据和 `prepare.json`/`finish.json` 诊断；
  不得把部分材料标记为成功。
- 真实 Agent 的启动和网络访问只发生在显式 record action 执行期间；
  默认测试使用受控子进程，不连接真实 Agent。

## 实现边界

plan source 复用 `protocol/capture` 的 stdio 采集和进程生命周期，不新增第三套
协议引擎；准备层负责隔离环境、配置、fixture、历史种子和 material 布局。`mock` 的
严格回放、prepared 生命周期和回执语义不变。

默认测试应覆盖 prepare 的环境/workspace/config、session/execution descriptor 不可篡改、
source 互斥、绝对路径、plan/agentType 不一致、原生参数白名单、execution 的帧和失败证据、finish 的
状态快照和哈希、`mock --describe --recording` 的 external/consumer 结论、
`prepare/record/mock/finish`、同一会话 record→mock、历史种子 allowlist、符号链接拒绝，以及运行间 UUID 变化的
负向用例。真实 Agent 检查只作为显式 opt-in，不计入默认离线门禁。

## 非目标

- 不执行 HTTP/SSE 或非 stdio 协议。
- 不模拟录制中的 shell、文件编辑、hooks 或模型调用及其副作用。
- 不安装真实 Agent、不执行登录或认证流程；只读取 agent type 标准配置位置和当前进程
  中已存在的允许项。
- 不启动消费者自己的 SDK 宿主；消费者应用 descriptor 后自行启动。
- 不把 consumer material 变成官方 case、官方验证结果或发布材料。
- v1 不实现 `probe` 或其他多连接录制。
