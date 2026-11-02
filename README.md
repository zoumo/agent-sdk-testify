# agent-sdk-testify

面向多语言 SDK 的 case、录制和 Agent mock 工具，对外只提供三个业务命令：

- `record`：按本仓库 case 录制真实 Agent。
- `verify`：用官方 SDK 对录制做离线验证。
- `mock`：使用录制充当 Agent，供消费者自己的测试连接。

本仓库维护 case、官方 driver、录制及材料库；`simulate`、Go driver 和 SDK 验收编排
留在 `agent-sdk-go`。SDK 与 MASS 直接消费本仓库的 mock。

设计已批准，已从 agent-sdk-go 复制文档、harness、代码、case 和已跟踪录制作为迁移基线。
工具现使用独立 module，源码位于仓库根目录，协议实现在根 `protocol/`。
当前 CLI 仍为待收敛的复制实现，三命令边界尚未实现。
[旧 CLI 参考](docs/legacy-cli.md)用于迁移期间，不代表最终公开接口。

- [mock 环境、运行描述与收尾接口](docs/mock-run.md)
- [已批准设计](docs/design.md)
- [实施规格与阶段规划](docs/implementation-plan.md)
- [导入来源与文件哈希](docs/import-baseline.json)
- [目标架构图（可缩放 HTML）](.archify/architecture-three-commands-20261102-181131/extraction.html)

## 独立构建与离线测试

使用 mise.toml 固定的 Go 1.26.9，不需要 Go SDK checkout、Go driver 或官方 SDK。
默认 Go 测试会构建自有工具进程；也可显式提供绝对路径 `AGENT_SDK_TESTIFY_BIN` 检查已有产物。

```sh
mise exec -- go build -o .artifacts/agent-sdk-testify .
mise exec -- go test ./...
mise exec -- go test -race ./...
mise exec -- go vet ./...
mise exec -- python3 -m unittest discover -s scripts -p 'test_*.py'
```

脚本测试和 wheel 构建需要 Python 3.11+、pip 和已安装的 setuptools>=68；不会安装官方 SDK。
`bash scripts/simulation.sh build` 仅构建本工具及官方 driver wheel。
旧 SDK simulate/gate 接入须由 SDK 提供 driver，不在本仓库构建。
SDK 专属断言原件与 T7 恢复要求见[待迁移材料](migration/agent-sdk-go/README.md)。
历史导入清单保持原路径与哈希，不随目录迁移改写；当前代码身份由 Git revision 确定。
常规检查全程离线，不执行真实 Agent 或新录制，也不构成 SDK 仿真通过的证据。

## 按 case 录制

在独立环境中先安装本仓库构建的官方 driver wheel 及所选 provider 的官方 SDK extra，
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

迁移期间，显式选择 `--materials`、`--cases-file` 或 `--index` 的旧流程仍按旧 CLI 执行，
将在 T8 统一移除；上面的新入口只使用本仓库 case 与已安装的官方 driver。
默认离线测试只以受控进程替换官方 driver 和 native Agent，不执行真实 SDK、模型或录制调用。

## 官方离线验证与采用

在已安装上述官方 driver 和 SDK 的环境中执行：

```sh
agent-sdk-testify verify --recording /private/new-capture \
  --output /private/new-verification --timeout 2m
```

provider/case 从录制身份确定，只选择本工具的官方 driver；不接受 `--driver`、
`--driver-arg`、`--peer` 或 SDK 仿真 driver。不会启动真实 Agent 或自动安装依赖。
原件与旧 replay 证据保持不变；新目录保存业务报告、每个选中连接的协议及退出回执、
失败诊断和 `verification.json`。验证证据绑定原件、合同、工具及 driver 哈希，
记录实际安装的官方 SDK 包和版本；版本不兼容须报告失败。
业务通过仍须所有连接严格匹配、完整消费、EOF、退出及清理通过。

新录制经验证后可直接沿用内部资格、私有归档与原子采用流程：

```sh
agent-sdk-testify verify --recording /private/new-capture \
  --output /private/new-verification \
  --index /absolute/recordings/index.json \
  --scenarios /absolute/scenarios --archive /private/archives
```

`--index` 启用采用；`--scenarios` 默认取 index 的相邻 `../scenarios`。
资格要求沿用既有隔离、来源、官方业务与材料规则，不生成新的内容审核或扫描流程。
`admission-summary.json` 单独报告采用结果；采用失败保留验证证据和旧索引/材料。
SDK simulate 不参与采用判断。资格与采用成功不单独证明内容允许公开发布。
