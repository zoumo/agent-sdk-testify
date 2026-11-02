# Extraction workspace

本仓库正在从 agent-sdk-go 的现有实现建立独立工具，先读
[已批准设计](docs/design.md)、[实施规格](docs/implementation-plan.md)和
[导入来源清单](docs/import-baseline.json)。

- 最终公开业务入口仅 record、verify、mock；simulate、Go driver 及 SDK 业务编排留在 agent-sdk-go。
- case、官方 driver、录制、材料及索引归本仓库。测试接入点为 mock 的进程接口。
- 继承的 harness 和工具代码当前仍保留旧 module、命令及 SDK 归属描述；这些是复制基线，
  冲突时以上述已批准边界为准，按实施规格逐步收敛，不将基线当成迁移完成。
- 保留录制原字节与来源；默认离线，不启动真实 Agent 或新增录制。
- Issue tracker：GitHub Issues，仓库 zoumo/agent-sdk-testify；规格 triage 标签 ready-for-agent。
  使用 gh CLI，发布后读回正文与标签。不得仅因 Git remote 变化而改换 tracker。

以下为从源仓库继承的工作指引；原 SDK 专属检查在后续迁移中调整归属：

# Agent guide

Read the standards relevant to the task:

- SDK, protocol, module, dependency or path changes: [development standards](harness/architecture.md).
- Tests, helpers or generators: [testing standards](harness/testing.md).
- Recording evidence: [recording and fixture provenance](tests/simulation/drivers/README.md#fixtures-仿真录制与测试输入).

Use the mise-managed compiler pinned in `mise.toml`. Preserve native protocol
contracts and recording provenance. Default validation is offline; this guide
does not authorize real agent execution or new recordings.

开始工作前读取 [harness 工作规则](harness/AGENTS.md)，设计从 [harness 入口](harness/index.md)读取。
