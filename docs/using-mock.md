# 从 SDK 测试使用 mock

`mock` 是 SDK 测试使用的原生 Agent 替身。SDK 仍按正常方式启动 Codex 或
Claude，`mock` 按录制检查每条客户端请求并返回对应响应。消费者负责 SDK 调用、
业务断言、远端服务和策略比较；工具只负责协议匹配、完整消费、退出和清理。

运行 `mock` 只需要安装好的 `agent-sdk-testify`，再选择一种材料来源：

- 内置 provider/case：无需源码、Python 或官方 SDK。
- 外部录制目录：使用绝对路径提供一份已有录制。

## 1. 选择材料

查询内置材料及可用性：

```sh
agent-sdk-testify mock --list --json
agent-sdk-testify mock --describe --provider codex --case SDK-CASE-001 --json
```

`--list` 返回全部 provider/case 身份，包括不可用项。每行包含
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

### assertionInputs

`describe.assertionInputs` 是 SDK 编写业务断言的输入，版本当前为 `1`：

- `schemaVersion` 当前为 `1`；`provider`、`caseId`、`attempt` 与 describe
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

## 2. 直接替换 Agent

只需要一个协议连接时，可直接让 SDK 把 `mock` 当作 Agent executable：

```sh
agent-sdk-testify mock --provider codex --case SDK-CASE-001 \
  --report /reports/direct.json

agent-sdk-testify mock --recording /captures/attempt \
  --report /reports/direct.json
```

`--report` 必须指向录制目录外一个尚不存在的持久文件；stdout 只承载原生 JSONL，
诊断写入 stderr。原生启动参数放在 `--` 之后。需要父进程环境、初始文件或
`main`/`probe` 多连接时，使用下面的 prepared 模式。

## 3. 准备 SDK 宿主

创建一次运行：

```sh
agent-sdk-testify mock --prepare --provider codex --case SDK-CASE-001 \
  --output /reports/new-run

agent-sdk-testify mock --prepare --recording /captures/attempt \
  --output /reports/new-run
```

`--output` 必须是一个尚不存在的绝对目录，父目录已存在，且不在录制目录内。
该目录是持久输出；`finish` 不会删除它。

命令返回完整 JSON descriptor，并把同一对象写入
`/reports/new-run/run.json`。把它当作只读数据，不要 source 或 eval。SDK 宿主
至少需要使用以下字段：

| 字段 | 用途 |
|---|---|
| `schemaVersion` | 当前为 `1`；不支持的版本必须拒绝 |
| `runId` | 本次运行的唯一身份 |
| `tool.command`, `toolVersion`, `toolSHA256` | 实际执行的工具及身份 |
| `recording`, `provider`, `caseId` | 本次选中的不可变材料 |
| `resourceRoot` | 工具拥有的临时根目录 |
| `workspace` | SDK 宿主的工作目录和可写 fixture |
| `protocolCwd` | Codex 原始项目 cwd；Claude 使用 `workspace` |
| `env` | 启动 SDK 宿主前需要合并的环境变量 |
| `contract`, `contractSHA256` | 原始 plan 及哈希 |
| `materialization`, `materializationSHA256` | 历史派生记录及哈希 |
| `connections` | 本次运行声明的全部协议连接 |
| `finish.command`, `finish.args` | 收尾命令 |

启动 SDK 宿主时，将 `workspace` 设为进程 cwd，并把 `env` 合并到宿主环境。
Codex 另用 `protocolCwd` 配置协议项目；Claude 使用 `workspace` 配置 WorkDir
和历史查询。只设置 mock 子进程的环境，不能配置父进程自己的历史 API。

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

运行一个连接：

```sh
agent-sdk-testify mock --run /reports/new-run/run.json
agent-sdk-testify mock --run /reports/new-run/run.json --connection probe
```

默认选择 `main`。`main` 和 `probe` 可以并发启动；同一连接不能重复启动。
每条连接都要独立完成严格匹配、完整消费、客户端 EOF、正常退出和清理。

如果 SDK 只接受 executable，使用 `launcher`。启动器单独接受 `--version` 或
`-v` 时，返回录制中的原生 CLI 版本，供 SDK 做兼容性查询；该查询不消耗连接，
也不写成功回执。它不是 `agent-sdk-testify --version` 的工具版本。

## 5. 关闭并收尾

SDK 测试结束或断言失败后，先关闭自己的 Session/Runtime，等待所有 peer 进程退出，
再执行：

```sh
agent-sdk-testify mock --finish /reports/new-run/run.json
```

`finish` 核验所有声明连接的身份、严格匹配、完整消费、EOF、未启动真实 Agent、
正常退出和历史哈希，然后删除工具拥有的临时根目录。它不会结束消费者的进程，
也不会删除输出目录、录制或调用方文件。

如果 peer 仍占用运行，`finish` 会失败并保留证据；关闭并等待 peer 后可以重试。
清理失败也保留在 `finish.json`/`cleanup.json` 中，可继续重试。即使清理成功，
失败的业务运行仍然是失败；`finish` 成功只表示协议和环境生命周期完成。

## 6. 结果文件

prepared 运行在 `--output` 下保留：

- `run.json`：本次运行的 descriptor。
- `<connection>.receipt.json`：协议匹配、完整消费、EOF、退出和错误。
- `<connection>.process.json`：监督进程看到的实际工具身份和退出状态。
- `finish.json`：全部连接、历史、清理和最终状态。
- `cleanup.json`：重复 finish 使用的清理证据。
- `prepare.json`：prepare 失败时的诊断和部分清理结果。

需要比较 preserve/suppress 等两套策略时，分别执行两次 `--prepare`，使用不同输出
目录、环境和回执。策略差异与 WebSocket bridge 由消费者负责。

业务断言、SDK 仿真、当前兼容性和完整覆盖分别由消费者报告。录制协议、退出和清理
通过不能替代 SDK 业务通过。
