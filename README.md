# agent-sdk-testify

面向多语言 SDK 的 case、录制和 Agent mock 工具，对外只提供三个业务命令：

- `record`：按本仓库 case 录制真实 Agent。
- `verify`：用官方 SDK 对录制做离线验证。
- `mock`：使用录制充当 Agent，供消费者自己的测试连接。

本仓库维护 case、官方 driver、录制及材料库；`simulate`、Go driver 和 SDK 验收编排
留在 `agent-sdk-go`。SDK 与 MASS 直接消费本仓库的 mock。

设计已批准，已从 agent-sdk-go 复制文档、harness、代码、case 和已跟踪录制作为迁移基线。
当前保留原目录布局与旧 module/CLI，三命令边界尚未实现，不能将复制完成视为工具拆分完成。

- [已批准设计](docs/design.md)
- [实施规格与阶段规划](docs/implementation-plan.md)
- [导入来源与文件哈希](docs/import-baseline.json)
- [目标架构图（可缩放 HTML）](.archify/architecture-three-commands-20261102-181131/extraction.html)
