# 从 SDK 测试使用 mock

`mock` 是 SDK 测试使用的原生 Agent 替身。SDK 仍按正常方式启动 Codex 或
Claude，`mock` 按录制检查每条客户端请求并返回对应响应。消费者负责 SDK 调用、
业务断言、远端服务和策略比较；工具只负责协议匹配、完整消费、退出和清理。

运行 `mock` 只需要安装好的 `agent-sdk-testify`，再选择一种材料来源：

- 内置 agentType/case：无需源码、Python 或官方 SDK。
- 外部录制目录：使用绝对路径提供一份已有录制，或使用
  [`prepare --plan` + record](consumer-recording.md) 生成一份消费者录制。

## 0. 消费者自己录制

外部消费者可以先用 `prepare --plan` 让工具创建可复用会话、隔离环境、workspace、
配置和历史种子，再由自己的 SDK 通过 record 驱动真实 Agent：

```sh
agent-sdk-testify prepare --agent codex \
  --plan /absolute/consumer-plan.json \
  --session-dir /absolute/session
```

prepare 在会话目录中写入 `session.json`，其中 record route 有稳定的 `env`、
workspace 和连接模板。消费者先应用这些值，再把 SDK 的 Agent executable 指向
record 命令：

```sh
agent-sdk-testify record --session-dir /absolute/session \
  --cmd /absolute/codex -- app-server --listen stdio://
```

工具负责隔离根、workspace、HOME、配置发现与筛选、tmp、历史种子和 material；
外部只提供 plan、Agent 启动命令/参数，并负责应用 route descriptor 和运行自己
的 SDK。它不能提供或覆盖配置目录、HOME、workspace 或原生环境变量。该模式不调用
官方 driver，也不生成官方 case 或验证证据。完整生命周期、plan 字段和可重复性要求
见[消费者录制](consumer-recording.md)。

record 完成后可以直接在同一会话执行 `mock --session-dir ...`，也可以先用
`mock --describe --recording <record-artifact> --json` 检查材料。consumer material
的结论始终是 `materialKind: external`、`qualification: unqualified` 和
`provenance: consumer`。

## 1. 选择材料

查询内置材料及可用性：

```sh
agent-sdk-testify mock --list --json
agent-sdk-testify mock --describe --agent codex --case SDK-CASE-001 --json
```

`--list` 返回全部 agentType/case 身份，包括不可用项。每行包含
`requirement`、`adoption`、`verification`、`available`、缺失原因、权威
`contract`/`binding`/`scenario`；可用项还会返回原始 `plan` 和材料身份。
`--describe` 只返回一个 case，不可用材料可以查看但不能启动。可用子集不等于
完整 required 覆盖或 SDK 验收通过。

外部录制直接查看材料：

```sh
agent-sdk-testify mock --describe --recording /captures/attempt --json
```

外部 describe 允许自定义 case ID 和未采用的合成录制，但仍校验 manifest、
plan、raw 和历史哈希。它的 `materialKind: external`、`qualification:
unqualified` 和原始 provenance 是明确结论，不会因为来源报告写了 passed 就变成
已采用材料。

材料自包含、只有一个 `main` 连接，且消费者宿主已经拥有与录制时一致的 cwd、HOME、
原生配置和历史时，直接使用 `mock --recording` 即可，不需要 prepared 会话。录制
材料本身就是 mock 的输入；`prepare` 不会改变录制内容，只用于在 SDK 宿主启动前
创建工具拥有的环境，并在会话结束时完成校验和清理。

### assertionInputs

`describe.assertionInputs` 是 SDK 编写业务断言的输入，版本当前为 `1`：

- `schemaVersion` 当前为 `1`；`agentType`、`caseId`、`attempt` 与 describe
  行一致，内置材料还带 `manifestSHA256`。
- `sourceManifestSHA256` 绑定来源 manifest；`missingEvidence` 列出缺失的可选
  证据，例如合成录制没有 `sourceReport`。
- `connections[]` 保留每条原生连接的顺序、`frames`、原始 JSONL
  `originalBase64` 和 SHA-256。
- `sourcePlan`、`sourceReport`、`savedHistory[]` 保留原始 JSON/JSONL 字节、
  哈希及解析后的 `value`/`rows`。
- `originalBase64` 是哈希输入，必须保留；解析 JSON 时保留未知字段和数字字面量。
- history ID 只是不透明来源标签，不能当成本地路径；运行中的历史读取使用
  descriptor 返回的 `workspace`/`materialization`。

SDK 应继续调用自己的公共方法；不要直接把捕获帧发给 mock，也不要另写 matcher。
不要自行打开 recording/raw/report 文件来替代 `assertionInputs`。

## 2. 直接替换 Agent（兼容别名）

规范形态是下一节的 prepared 会话。只需要一个协议连接且宿主环境已经一致时，
可以直接让 SDK 把 `mock` 当作 Agent executable：

```sh
agent-sdk-testify mock --agent codex --case SDK-CASE-001 \
  --report /reports/direct.json

agent-sdk-testify mock --recording /captures/attempt \
  --report /reports/direct.json
```

`--report` 必须指向录制目录外一个尚不存在的持久文件；stdout 只承载原生 JSONL，
诊断写入 stderr。原生启动参数放在 `--` 之后。需要父进程环境、初始文件或
`main`/`probe` 多连接时，使用下面的 prepared 会话。

## 3. Prepared 会话

先创建会话：

```sh
agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
  --session-dir /reports/session

agent-sdk-testify prepare --recording /captures/attempt \
  --session-dir /reports/session
```

`--session-dir` 必须是一个尚不存在的绝对目录，父目录已存在，且不在录制目录内。
prepare 创建该目录；后续 action 和 `finish` 使用同一路径。该目录是工具持久保存的
控制面和证据根，不是 Agent 的 home 或 `CODEX_HOME`/`CLAUDE_CONFIG_DIR`；
`finish` 不会删除它。

prepare 在会话目录中写入 `session.json`，其中包含 source、agentType、环境基线、
允许 action 和 route 环境。把它当作内部只读数据，不要 source 或 eval；CLI 只接受
会话目录，不接受 `session.json` 文件路径。SDK 宿主至少需要使用以下字段：

| 字段 | 用途 |
|---|---|
| `schemaVersion` | 当前为 `1`；不支持的版本必须拒绝 |
| `sessionId` | 本次会话的唯一身份 |
| `tool.command`, `toolVersion`, `toolSHA256` | 实际执行的工具及身份 |
| `source`, `sourceSHA256`, `agentType` | 本次会话的不可变来源 |
| `resourceRoot` | 工具拥有的临时根目录 |
| `routes` | record、verify、mock 等可用 route |
| 每个 route 的 `workspace` | SDK 宿主的工作目录和可写 fixture |
| 每个 route 的 `env` | 启动 SDK 宿主前需要合并的环境变量 |
| 每个 route 的 `connections` | 该 action 声明的全部协议连接 |
| `finish.command`, `finish.args` | 整个会话的收尾命令 |

启动 SDK 宿主时，将 `workspace` 设为进程 cwd，并把 `env` 合并到宿主环境。
Codex 另用 `protocolCwd` 配置协议项目；Claude 使用 `workspace` 配置 WorkDir
和历史查询。只设置 mock 子进程的环境，不能配置父进程自己的历史 API。

每个 record、verify 或 mock action 会另建 `executions/<executionId>/execution.json`。它记录本次
action、输入 artifact、workspace overlay、env、connections、receipts 和 limits。
同一会话可以执行多个 execution；每个 execution 从环境基线创建独立 overlay，后续
verify 或 mock 默认消费最新的 record capture。同一会话的 action 串行执行；同一
execution 的连接可以并发。

`materialization.json` 记录历史副本的来源、派生路径和双向哈希。Claude 只替换
必要的顶层 cwd/project 身份，保留 ID、未知字段及数字字面量。只有 manifest
绑定的历史或 plan 绑定的 history seed 会被采用。

## 4. 启动连接

每个连接包含：

| 字段 | 用途 |
|---|---|
| `id` | `main`；远端关闭流程还可能有 `probe` |
| `command`, `args` | 可直接启动的原生协议命令 |
| `launcher` | 只能接收 executable 的 SDK 使用的临时启动器 |
| `recording` | 本条连接对应的录制 |
| `planSHA256`, `manifestSHA256`, `recordingSHA256` | 材料身份 |
| `receipt`, `processReceipt` | 协议回执和进程回执的持久路径 |

普通流程只有 `main`。多连接用于真实的多个独立客户端生命周期，不是把一条 Agent
协议拆成多条通道。当前只有官方 `remote-close`（SDK-CASE-032）声明 `main` 和
`probe`：`main` 创建并进行交互，随后主动断开；`probe` 作为第二个独立客户端重新
连接同一远端服务并读取已保存结果，用来证明第一个连接关闭后服务仍然可用。两条连接
需要各自的 raw、receipt 和 EOF 证据，因此不能在 mock 中合并。

运行一个 mock 连接：

```sh
agent-sdk-testify mock --session-dir /reports/session
agent-sdk-testify mock --session-dir /reports/session --connection probe
```

默认选择最新兼容 capture 和 `main`。`main` 和 `probe` 可以并发启动；同一 execution 的
同一连接不能重复启动。需要消费指定的旧 capture 时使用 `--recording PATH`。
每条连接都要独立完成严格匹配、完整消费、客户端 EOF、正常退出和清理。

如果 SDK 只接受 executable，使用 `launcher`。启动器单独接受 `--version` 或
`-v` 时，返回录制中的原生 CLI 版本，供 SDK 做兼容性查询；该查询不消耗连接，
也不写成功回执。它不是 `agent-sdk-testify --version` 的工具版本。

## 5. 关闭并收尾

SDK 测试结束或断言失败后，先关闭自己的 Session/Runtime，等待所有 peer 进程退出，
再执行：

```sh
agent-sdk-testify finish --session-dir /reports/session
```

`finish` 逐 execution 核验所有声明连接的身份、严格匹配、完整消费、EOF、未启动真实
Agent、正常退出和历史哈希，然后删除工具拥有的环境基线和临时根目录。它不会结束消费者的进程，
也不会删除输出目录、录制或调用方文件。

如果 peer 仍占用运行，`finish` 会失败并保留证据；关闭并等待 peer 后可以重试。
清理失败也保留在 `finish.json`/`cleanup.json` 中，可继续重试。即使清理成功，
失败的业务运行仍然是失败；`finish` 成功只表示协议和环境生命周期完成。

## 6. 结果文件

prepared 会话在 `--session-dir` 目录下保留：

- `session.json`：会话的 source、agentType 和 route 环境。
- `executions/<executionId>/execution.json`：本次 action 的 descriptor。
- `executions/<executionId>/<connection>.receipt.json`：协议匹配、完整消费、EOF、退出和错误。
- `executions/<executionId>/<connection>.process.json`：监督进程看到的实际工具身份和退出状态。
- `executions/index.json`：append-only execution/artifact 索引及最新 capture。
- `finish.json`：全部 execution、历史、清理和最终状态。
- `cleanup.json`：重复 finish 使用的清理证据。
- `prepare.json`：prepare 失败时的诊断和部分清理结果。

需要比较 preserve/suppress 等两套策略时，在同一会话创建不同 mock execution，或分别
prepare 两个会话，使用不同输出目录和回执。策略差异与 WebSocket bridge 由消费者负责。

业务断言、SDK 仿真、当前兼容性和完整覆盖分别由消费者报告。录制协议、退出和清理
通过不能替代 SDK 业务通过。
