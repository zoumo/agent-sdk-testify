# agent-sdk-testify

面向多语言 SDK 的 case、录制和 Agent mock 工具，对外只提供三个业务命令：

- `record`：按本仓库 case 录制真实 Agent。
- `verify`：用官方 SDK 对录制做离线验证。
- `mock`：使用录制充当 Agent，供消费者自己的测试连接。

本仓库维护 case、官方 driver、录制及材料库；`simulate`、Go driver 和 SDK 验收编排
留在 `agent-sdk-go`。SDK 与 MASS 直接消费本仓库的 mock。

工具使用独立 module，仓库根目录只保留 `main.go`；命令实现按功能位于
`internal/`，协议位于 `protocol/`。权威场景、采用材料和内置快照统一位于
`data/`，官方 driver 与 wheel 位于 `drivers/`。
已实现的业务入口只有 record、verify、mock；不提供 simulate/replay/corpus/protocol/peer 入口。
内部协议子进程仅供官方录制与验证使用。当前版本 `0.1.0-dev` 是本地开发版本，尚未发布。

## 文档

| 主题 | 文档 |
| --- | --- |
| 工具边界、合同与材料链路 | [工具设计](docs/design.md) |
| 模块、依赖、布局、编码与测试 | [开发规范](docs/development.md) |
| 8 个用户故事与 36 个 case | [共同验收](docs/cases.md) |
| 从 SDK 测试接入、查询材料、运行与收尾 mock | [mock 使用指南](docs/using-mock.md) |
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
agent-sdk-testify record --provider codex --case SDK-CASE-001 \
  --cli /absolute/codex --config /absolute/selected-config \
  --output /private/new-capture
```

case 与 binding 随二进制嵌入，可离开源码目录使用；不需要 `--materials` 或 `--driver`。
`--config` 仅选取既有允许的配置与认证，个人 skills、插件和指令不会整体复制。
可选 `--model`、`--initial-model`、`--history-seed` 与 `--timeout` 沿用录制规则。
省略 output 时创建持久私有 attempt，显式 output 必须尚不存在。
失败与未触发尝试仍保留原始 plan/raw、业务状态、stderr、采集与清理回执；准备阶段失败保留诊断。
录制通过不等于官方验证、审核采用或发布通过，record 不自动执行这些后续步骤。

默认离线测试只以受控进程替换官方 driver 和 native Agent，不执行真实 SDK、模型或录制调用。

## 官方离线验证与采用

在已安装上述官方 driver 和 SDK 的环境中执行：

```sh
agent-sdk-testify verify --recording /private/new-capture \
  --output /private/new-verification --timeout 2m
```

provider/case 从录制身份确定，只选择本工具的官方 driver；不接受 `--driver`、
`--driver-arg`、`--peer` 或 SDK 仿真 driver。不会启动真实 Agent 或自动安装依赖。
原件与已有 replay 证据保持不变；新目录保存业务报告、每个选中连接的协议及退出回执、
失败诊断和 `verification.json`。验证证据绑定原件、合同、工具及 driver 哈希，
记录实际安装的官方 SDK 包和版本；版本不兼容须报告失败。
业务通过仍须所有连接严格匹配、完整消费、EOF、退出及清理通过。

新录制经验证后可直接沿用内部资格、私有归档与原子采用流程：

```sh
agent-sdk-testify verify --recording /private/new-capture \
  --output /private/new-verification \
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

消费者先 `mock --describe`，按 [mock 使用指南](docs/using-mock.md) prepare、执行自身业务、关闭宿主，
最后 finish。SDK/MASS 的业务成功与工具协议成功分别判断。MASS 的 mass/2 Codex
握手与录制中的官方客户端不同时应失败，不得修改录制或放宽字段。
[driver 环境与已知限制](drivers/README.md#官方-sdk-环境)记录实际依赖和来源。
