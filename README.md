# agent-sdk-testify

Testify 录制原生 Agent 协议，并用录制回放协议，供 SDK 测试使用。仓库维护 Codex 和 Claude 的 case、官方 driver、录制材料与内置快照。消费者用自己的 SDK 执行业务操作和断言。

<a id="文档"></a>

## 按任务查文档

| 任务 | 文档 |
| --- | --- |
| 从内置 case 回放 | [准备内置材料](docs/using-mock.md#准备材料与-fixture) |
| 回放已有 recording | [导入 recording](docs/using-mock.md#准备材料与-fixture) |
| 录制自己的 SDK 交互 | [使用 consumer plan 录制并回放](docs/consumer-recording.md) |
| 在 Go 测试中回放 | [Go 库入口](#go-库入口) |
| 用官方 SDK 录制、验证或采用材料 | [官方 driver 操作指南](drivers/README.md#按-case-录制) |
| 构建工具、运行离线测试或发布 | [从源码构建与验证](docs/development.md#从源码构建与验证) |

## Go 库入口

导入 `github.com/zoumo/agent-sdk-testify`，用 `Fixture.StartMockAgent` 在 Go 测试中回放。
启动、半关闭与收尾顺序见[Go 库回放合同](docs/library-entry-spec.md#启动-handle)。
公开类型和函数见[根包源码](library.go)。

<a id="独立构建与离线测试"></a>
<a id="构建交付与消费者"></a>

## 从源码构建

在仓库根目录运行 `make build`。默认产物是 `bin/agent-sdk-testify`。开发规范列出依赖、linked worktree 的构建条件、离线命令与发布步骤。[构建与验证](docs/development.md#从源码构建与验证)

<a id="按-case-录制"></a>
<a id="官方离线验证与采用"></a>

## 用官方 SDK 录制与验证

使用 `official record` 采集官方 case，用 `official verify` 回放并检查官方 SDK 的业务断言。官方 driver 环境、命令及采用条件见操作指南。[官方 driver 操作指南](drivers/README.md#按-case-录制)

## 参考文档

[文档索引](docs/index.md)列出 `docs/` 下的指南与合同。

| 查阅内容 | 文档 |
| --- | --- |
| CLI 职责、fixture、execution 和材料 | [工具设计](docs/design.md#命令与数据边界) |
| Go 进程内回放与执行报告 | [Go 库回放合同](docs/library-entry-spec.md) |
| SDK case 与验收标准 | [场景目录](data/scenarios)、[共同验收](docs/cases.md) |
| 录制材料的身份、来源和可用状态 | [固定索引](data/index.json)、[内置快照](data/builtin/manifest.json) |
| 模块、测试层级和编码规则 | [开发规范](docs/development.md) |
| 文档术语 | [术语表](GLOSSARY.md) |
