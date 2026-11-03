# agent-sdk-testify

面向多语言 SDK 的 case、录制和 Agent mock 工具。公开 CLI 入口为：

- `prepare`：按 case、consumer plan 或已有录制创建一个可复用隔离会话。
- `record`：在 prepared 会话中按 case 或 consumer plan 录制真实 Agent。
- `verify`：用官方 SDK 对录制做离线验证。
- `mock`：使用录制充当 Agent，供消费者自己的测试连接。
- `finish`：汇总会话内全部 execution 的证据、写 closed 状态并清理工具资源。

`record`、`verify`、`mock` 是业务动作；`prepare`、`finish` 是环境会话的独立生命周期
阶段。

本仓库维护 case、官方 driver、录制及材料库；`simulate`、Go driver 和 SDK 验收编排
留在 `agent-sdk-go`。SDK 与 MASS 直接消费本仓库的 mock。

工具使用独立 module，仓库根目录只保留 `main.go`；命令实现按功能位于
`internal/`，协议位于 `protocol/`。权威场景、采用材料和内置快照统一位于
`data/`，官方 driver 与 wheel 位于 `drivers/`。
不提供 simulate/replay/corpus/protocol/peer 业务入口。
内部协议子进程仅供官方录制与验证使用。当前版本 `0.1.0-dev` 是本地开发版本，尚未发布。

规范流程是 `prepare → record/verify/mock → finish`。一次 prepare 可以承载多个 action
execution；record 生成的 capture 会登记到会话，后续 verify 和 mock 默认消费最新的
兼容 capture，verify 不会消费掉它。每个 execution 使用独立可写 overlay，不污染环境
基线或另一个 execution。source（case/plan/recording）、executor（官方 driver/消费者 SDK）和 target
（真实 Agent/严格回放）分别在 prepare 和具体 action 时确定，不再使用 `--consumer`
开关。

## 文档

| 主题 | 文档 |
| --- | --- |
| 工具边界、合同与材料链路 | [工具设计](docs/design.md) |
| 模块、依赖、布局、编码与测试 | [开发规范](docs/development.md) |
| 8 个用户故事与 36 个 case | [共同验收](docs/cases.md) |
| 从 SDK 测试接入、查询材料、运行与收尾 mock | [mock 使用指南](docs/using-mock.md) |
| 外部消费者自己录制并用 mock 回放 | [消费者录制](docs/consumer-recording.md) |
| 官方 driver、SDK 环境和已知限制 | [driver 说明](drivers/README.md#官方-sdk-环境) |
| 术语 | [GLOSSARY](GLOSSARY.md) |

## 独立构建与离线测试

使用 Go 1.26 和 golangci-lint 2.12.2，不需要 Go SDK checkout、Go driver 或官方 SDK。
默认 Go 测试会构建自有工具进程；也可显式提供绝对路径 `AGENT_SDK_TESTIFY_BIN` 检查已有产物。

```sh
make build
make test
make test-race
make vet
make lint
make fmt
```

脚本测试和 wheel 构建需要 Python 3.11+、pip 和已安装的 setuptools>=68；不会安装官方 SDK。
`make bundle` 仅构建本工具及官方 driver wheel。
SDK 自己构建 simulate/gate，通过显式 `AGENT_SDK_TESTIFY_BIN` 消费 mock；发布 pin 仍为 null。
常规检查全程离线，不执行真实 Agent 或新录制，也不构成 SDK 仿真通过的证据。

## 按 case 录制

按 [driver 环境说明](drivers/README.md#官方-sdk-环境)显式安装本仓库 wheel 和所选官方 SDK；Codex 使用固定官方源码及 `--no-deps`，
将该环境的 bin 目录放入 PATH。工具内部选择 `agent-sdk-testify-codex` 或
`agent-sdk-testify-claude`；不会下载、安装或改用其他 SDK 包。
安装需满足该官方 SDK 版本的 Python 与包源要求；wheel 本身安装成功不证明 SDK 依赖可用。

```sh
agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
  --session-dir /private/session
agent-sdk-testify record --session-dir /private/session \
  --cmd /absolute/codex -- app-server --listen stdio://
agent-sdk-testify verify --session-dir /private/session
agent-sdk-testify mock --session-dir /private/session
agent-sdk-testify finish --session-dir /private/session
```

case 与 binding 随二进制嵌入，可离开源码目录使用；不需要 `--materials` 或 `--driver`。
配置和认证由工具从 agent type 标准配置位置发现并筛选，不整体复制个人 skills、插件或指令。
`--model`、`--initial-model`、`--history-seed` 等环境选项属于 prepare；
`--timeout` 等执行选项属于具体 action。所有 prepared-session 命令的
`--session-dir` 都指向同一个会话目录；prepare 要求该目录尚不存在并负责创建，
后续命令读取该目录。`session.json` 是工具内部 descriptor，不是 CLI 参数。
失败与未触发尝试仍保留原始 plan/raw、业务状态、stderr、采集与清理回执；准备阶段失败保留诊断。
录制通过不等于官方验证、审核采用或发布通过，record 不自动执行这些后续步骤。

默认离线测试只以受控进程替换官方 driver 和 native Agent，不执行真实 SDK、模型或录制调用。

外部消费者可以 `prepare --plan`，再用 record 让自身 SDK 驱动真实 Agent。工具准备
隔离环境、workspace、配置和历史种子，消费者只提供真实 Agent 启动命令/参数并在 SDK
宿主应用 route descriptor；record 产物可在同一会话中直接 mock。该路径不经过官方
driver，也不进入官方 case、验证、采用或内置快照；合同和用法见
[消费者录制](docs/consumer-recording.md)。

## 官方离线验证与采用

在已安装上述官方 driver 和 SDK 的环境中执行：

```sh
agent-sdk-testify prepare --agent codex \
  --recording /private/new-capture --session-dir /private/session
agent-sdk-testify verify --session-dir /private/session --timeout 2m
```

agent type/case 从录制身份确定，只选择本工具的官方 driver；不接受 `--driver`、
`--driver-arg`、`--peer` 或 SDK 仿真 driver。不会启动真实 Agent 或自动安装依赖。
原件与已有 replay 证据保持不变；会话中的 verify execution 保存业务报告、每个选中连接的
协议及退出回执、失败诊断和 `verification.json`。验证证据绑定原件、合同、工具及 driver 哈希，
记录实际安装的官方 SDK 包和版本；版本不兼容须报告失败。
业务通过仍须所有连接严格匹配、完整消费、EOF、退出及清理通过。

新录制经验证后可直接沿用内部资格、私有归档与原子采用流程：

```sh
agent-sdk-testify verify --session-dir /private/session \
  --recording /private/new-capture \
  --index /absolute/data/index.json \
  --scenarios /absolute/data/scenarios --archive /private/archives
```

`--index` 启用采用；`--scenarios` 默认取 index 的相邻 `../scenarios`。
资格要求沿用既有隔离、来源、官方业务与材料规则，不生成新的内容审核或扫描流程。
`admission-summary.json` 单独报告采用结果；采用失败保留验证证据和旧索引/材料。
SDK simulate 不参与采用判断。资格与采用成功不单独证明内容允许公开发布。

## 构建交付与消费者

`make release OUTPUT=/absolute/new-dist` 生成 Linux/macOS
amd64/arm64 独立归档、官方 driver wheel、manifest.json 和 SHA256SUMS。
先显式准备 Go module 与 Python 构建依赖；脚本不会下载依赖、推送或发布。
版本、工具源码 revision、内置材料 revision/哈希和逐产物校验和分别记录。
运行 `make check` 执行离线完整门禁；CI 分开准备依赖后执行同一入口。

消费者先 `mock --describe`，按 [mock 使用指南](docs/using-mock.md) prepare、在会话中
选择 mock、执行自身业务、关闭宿主，最后 finish。SDK/MASS 的业务成功与工具协议成功
分别判断。MASS 的 mass/2 Codex
握手与录制中的官方客户端不同时应失败，不得修改录制或放宽字段。
[driver 环境与已知限制](drivers/README.md#官方-sdk-环境)记录实际依赖和来源。
