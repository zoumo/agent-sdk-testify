# 消费者录制

外部消费者可以使用 `prepare --plan` 创建会话，再由 record 把自身 SDK 或运行时发往
真实 Agent 的原生交互保存为 consumer material，并在同一会话中直接用 mock 严格
回放。该路径不经过官方 driver，不登记官方 case，也不进入验证、采用或内置快照。

consumer 录制采用 `prepare → agent record → agent mock → finish` 生命周期。一次
prepare 可以承载多个 record/mock execution；环境准备和材料由工具完整负责，
消费者只提供 plan、真实 Agent 启动命令/参数，并在自己的 SDK 宿主演用当前 route 的
环境。本文只描述 plan source；生命周期、source/executor/target 和 descriptor 合同见
[工具设计](design.md#会话生命周期)。

## 边界

- 工具负责隔离根、workspace、HOME、配置发现与筛选、tmp、历史种子、材料写入和清理。
- 消费者负责 plan、真实 Agent executable 和启动参数、SDK 宿主、业务断言和收尾调用。
- 消费者不能提供或覆盖隔离根、workspace、HOME、config、tmp 或原生环境变量。
- 工具不启动消费者的 SDK 宿主，也不生成客户端请求；客户端始终是消费者自己的 SDK。
- 材料是 external/consumer 材料，只能通过 session 的 current 消费，不参与官方资格、采用和发布。
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

配置来源、允许文件、认证变量和隔离路径由[工具设计](design.md#环境与隔离)统一维护。
消费者不能通过 flag 覆盖这些路径，只使用 prepare 返回的 descriptor；认证值不进入
`session.json`，调用进程只需保留 record route `runtimeEnv` 列出的变量。

### agent record

```sh
agent-sdk-testify agent record --session-dir /absolute/session \
  [--timeout DURATION] \
  --cmd /absolute/codex -- app-server --listen stdio://
```

消费者必须先应用 record route 的 `env`，把 SDK 宿主 cwd 设为 `workspace`，然后执行
下文的 `agent record` 命令；Agent executable 和原生参数属于这次显式调用。record route 的
`connection.command`/`args` 是稳定调用前缀，其 `args` 已包含 `--cmd`，调用方在其后
追加 `/absolute/agent -- ARG...`。route 的 `runtimeEnv` 只列出来自调用进程或私有
运行环境的变量名，不包含值。record 从环境基线创建独立 execution overlay，在已准备
好的环境中启动真实 Agent，转发 stdio，并写入原始帧和传输报告。每个连接只能启动
一次；失败也保留原始证据。

record 成功后发布不可变 material 并原子替换 current recording；随后可以直接在同一
会话执行 `agent mock`，不需要重新 prepare。失败保留 execution，current 不变。record source
保持 prepare 固化的 plan 和 executor，不从 current 推导。consumer material 没有适用
官方 verifier，verify 在启动 driver 前失败。

### agent mock

```sh
agent-sdk-testify agent mock --session-dir /absolute/session \
  --connection main
```

mock 只消费会话中的 current recording，使用新的 execution overlay 和预先刷新的回放
环境。消费者按 mock route 应用 `env`、cwd 和 `connection`，关闭 SDK 后继续 finish。

### finish

```sh
agent-sdk-testify finish --session-dir /absolute/session
```

消费者先关闭自己的 SDK、Session 和 Runtime，等所有 peer 退出，再调用 `finish`。
`finish` 逐 execution 校验 record 和 mock、重新核验历史状态、manifest/report 和哈希，
然后删除工具拥有的临时根。工具不会结束消费者的进程，也不会删除会话目录。
finish 忙碌时不写状态或清理；取得锁后立即关闭会话。清理失败不重新打开会话，重试
只处理未完成清理，不重新解释业务结果。材料保留，closed 后仍可 export。

## 多次使用

- record 成功后，mock 使用新的 current；可在同一会话直接执行。
- 多次 record 产生多个不可变 material，旧材料和旧验证证据保留；action 不接受材料覆盖。
- 需要在另一 session 使用材料时，先 export，再 prepare --recording；导出不提升可信等级。
- 每次 record 或 mock 使用独立 execution overlay，前一次 SDK 的历史和写入不会污染后一次。
- 同一会话的 record/mock execution 串行执行；上一个 execution 完成后才可以开始下一个。
- consumer capture 不进入 verify；需要官方验证时必须重新走官方 case 录制。

环境职责和认证边界见[工具设计](design.md#环境与隔离)。本路径的差异只有：消费者提供
plan、真实 Agent executable/args、自己的 SDK 宿主和业务断言。

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
| `cliVersion` | 可选字符串 | 来源记录中的原生 CLI 版本；mock 不模拟版本查询 |
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

`session.json` 和 `execution.json` 的字段合同见
[工具设计](design.md#会话生命周期)。plan source 的 `protocolCwd` 保持
plan 声明的原始项目身份；消费者仍使用 execution 返回的 overlay `workspace`。

每次 record 或 mock 另在 `executions/<executionId>/execution.json` 写入本次
execution 的只读合同，字段见[工具设计](design.md#会话生命周期)。
消费者不得 source、eval 或改写 `session.json` 或 `execution.json`。当前 route 的
`env` 必须应用到自己的 SDK 宿主，不能只设置录制或 mock 子进程。execution 创建后
不得重新生成或覆盖。

finish 删除工具临时根后，其中 `resourceRoot` 等路径失效。会话目录里的材料仍可 export，
并由新 session 的 prepare --recording 导入；closed session 不能创建新的 action execution。

mock route 的 `connection.launcher` 用于只接受 executable 的 SDK；launcher 把受支持
的追加参数转发给回放连接，并拒绝重复或白名单外的参数。record 使用上文的显式
`agent record --cmd ... --` 调用，不把 route 前缀当作完整 executable。

## 原生启动参数

v1 复用 `agent mock` 的原生启动参数白名单，避免录出一份无法回放的材料：

- Codex：`app-server`、`--listen stdio://`。
- Claude：`--output-format stream-json`、`--input-format stream-json`、
  `--permission-prompt-tool stdio`，可选 `--verbose`、
  `--include-partial-messages`、`--model=...`、`--resume=...`。

重复参数、未知参数和空格形式的 `--model VALUE`/`--resume VALUE` 在 record 启动前失败。
需要其他原生启动参数时，先扩展回放 adapter 和白名单；不能只放宽录制侧。

## 材料布局

record 的尝试与失败证据保存在 execution；成功材料发布到独立 material 目录：

```text
<session>/materials/<materialId>/
  material.json
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

- `materials/<materialId>/` 是不可变材料根；`current.json` 选择当前材料。
- `executions/<executionId>/execution.json` 和其 `record/` 保留本次尝试与失败证据。
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

## 协议执行结果

`agent record` 和 `agent mock` 都接受可选的 `--result-fd N`（N ≥ 3）。
调用方为该次进程传入独立的可写文件或管道描述符；协议 stdout 不包含结果通知。
例如，在已应用 route 环境的宿主中保存一次调用的结果：

```sh
agent-sdk-testify agent record --session-dir /absolute/session \
  --result-fd 3 --cmd /absolute/agent -- app-server --listen stdio:// \
  3>/absolute/record-result.jsonl
```

领取 execution 后，命令先写入一条 `status: running` JSONL 通知，结束时再写入
`passed` 或 `failed` 通知。领取前失败不会通知或创建 execution。每条通知包含
`sessionId`、`executionId`、`connectionId`、`command`、`execution` descriptor 路径、
`receipt` 和持久 `result` 路径；mock 还包含 `processReceipt`。最终通知包含
`executionSHA256`；成功 record 和所有 mock 通知中的 `material` 包含精确材料 ID、
路径及 plan、manifest、raw 哈希。record 失败不把未发布材料当成成功材料引用。

即使不传 `--result-fd`，同一结果仍保存在
`executions/<executionId>/receipts/<connectionId>.result.json`。
结果通知通过本次进程明确关联身份，调用方不得扫描最新目录来猜测归属。
单个 connection 的 `passed` 不代表同一 execution 的其他 connection 已完成；
完整状态以 execution index 和各 receipt 为准。多连接调用分别使用独立结果描述符。
通知写入响应取消，每条通知最多等待 1 秒；
管道无人读取或已关闭时命令失败，持久结果文件仍可读取。最终通知在取消后仍作
最多 1 秒的有界写入。描述符只用于通知，不得与协议或其他输出共享。
launcher 只转发原生参数；需要结果通知时，使用 route 的 `command` 和 `args`
直接启动，并在原生参数分隔符之前传 `--result-fd`。

record 在采集、材料闭包和清理完成后发布 current，并返回自身结果，不等待父 SDK
宿主的业务报告。case source 的直接 agent record 也只代理真实 Agent；其材料保持
external provenance，官方业务事实由 official 编排另行保存和核验。

## 使用 mock

```sh
agent-sdk-testify prepare \
  --recording /absolute/material --session-dir /absolute/session
agent-sdk-testify agent mock --session-dir /absolute/session
agent-sdk-testify finish --session-dir /absolute/session
```

材料的 consumer provenance 不因导入或导出而提升。只有 prepare 接受材料路径，mock
只接受 session 和本次执行参数。

消费者仍负责 SDK 调用、业务断言、策略比较和真实 E2E。`official verify` 只接受官方 driver
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
快照。需要官方采用时，必须用 `prepare --case` 和 `official record` 重新采集，再走 `official verify`、
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

plan source 复用 `internal/protocol/capture` 的 stdio 采集和进程生命周期，不新增第三套
协议引擎；准备层负责隔离环境、配置、fixture、历史种子和 material 布局。`agent mock` 的
严格回放、prepared 生命周期和回执语义不变。

默认测试应覆盖 prepare 的环境/workspace/config、session/execution descriptor 不可篡改、
source 互斥、绝对路径、plan/agentType 不一致、原生参数白名单、execution 的帧和失败证据、finish 的
状态快照和哈希、consumer 材料不具备官方 verifier、
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
