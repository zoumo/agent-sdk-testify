# Agent guide

本仓库维护独立的 record、verify、mock 工具。先读[工具设计](docs/design.md)，再按任务读取：

- SDK、协议、模块、依赖、路径、测试或生成器：[开发规范](docs/development.md)
- 用户故事与 case：[共同验收](docs/cases.md)
- 录制材料与来源：[driver 与测试输入](drivers/README.md#fixtures-仿真录制与测试输入)

## 仓库边界

- 最终公开业务入口仅 record、verify、mock；simulate、Go driver 及 SDK 业务编排留在 agent-sdk-go。
- case、官方 driver、录制、材料及索引归本仓库。测试接入点为 mock 的进程接口。
- 保留录制原字节与来源；默认离线，不启动真实 Agent 或新增录制。
- SDK 参考合同仅定义消费者行为；SDK 编排由 SDK 仓库负责。
- Issue tracker：GitHub Issues，仓库 zoumo/agent-sdk-testify；规格 triage 标签 ready-for-agent。
  使用 gh CLI，发布后读回正文与标签。不得仅因 Git remote 变化而改换 tracker。

## 文档规则

- 设计与规范修改须有人类明确要求或事先同意；授权包含必需的引用同步。
- 设计、规范和共同验收集中在 `docs/`；术语维护在根 [GLOSSARY.md](GLOSSARY.md)。
  使用命令、原生来源与证据放在对应 README、来源文件和工具说明。
- 每项规则只维护一处；补充现有文档，不为单次任务创建报告或子索引。历史由 Git 保存。
- 正文描述确定的目标合同，不保留讨论过程或实现进度，也不把设计目标当成实现或验证结果。
- 使用相对链接。

使用 `mise.toml` 固定的编译器。保留原生协议合同与录制来源；默认验证离线，
不得据此执行真实 Agent 或新增录制。
