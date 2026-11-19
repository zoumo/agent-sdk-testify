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

导入 `github.com/zoumo/agent-sdk-testify`。包名为 `agenttestify`。在测试中用 `Prepare` 创建 fixture，用 `Open` 读取已有 fixture，用 `Export` 导出 current recording。外部进程流使用 `Fixture.RecordAgent` 或 `Fixture.MockAgent`。官方录制和验证从 CLI 启动。

1. 调用 `fixture.StartMockAgent`。传入 `StartMockAgentStreams` 的两个可关闭协议端点。
2. 在协议读写前，读取 `run.Start`。用 `ExecutionStart` 配置本次 SDK 的环境、工作目录和协议项目身份。
3. 运行 SDK 测试，同时等待 `run.FullConsumption()` 和 `run.Done()`。若先收到完整消费通知，只关闭 SDK 写往 mock 的一端。继续读取 mock 输出，直到 EOF。若执行提前结束，直接读取 `Wait` 的结果。
4. 调用 `run.Wait(ctx)`。检查错误和 `result.Execution`。整个 execution 通过时，报告的 `Complete` 为 `true`，`Status` 为 `agenttestify.ExecutionStatusPassed`。
5. 所有声明的连接结束后，关闭 SDK 并清理 fixture。多连接执行的最终汇总用 `fixture.ExecutionReport(executionID)` 读取。

`agent-sdk-go` 的测试和仿真在进程内链接固定版本的 Testify Go 库。`AGENT_SDK_TESTIFY_BIN` 用于 Testify 自己的 CLI 测试，可复用已构建的产物。

查阅进程流、半关闭、报告和 `PrepareOptions.EmbeddedIdentity` 的字段与示例。[Go 库 API 规格](docs/library-entry-spec.md)

<a id="独立构建与离线测试"></a>
<a id="构建交付与消费者"></a>

## 从源码构建

在仓库根目录运行 `make build`。默认产物是 `bin/agent-sdk-testify`。开发规范列出依赖、linked worktree 的构建条件、离线命令与发布步骤。[构建与验证](docs/development.md#从源码构建与验证)

<a id="按-case-录制"></a>
<a id="官方离线验证与采用"></a>

## 用官方 SDK 录制与验证

使用 `official record` 采集官方 case，用 `official verify` 回放并检查官方 SDK 的业务断言。官方 driver 环境、命令及采用条件见操作指南。[官方 driver 操作指南](drivers/README.md#按-case-录制)

## 参考文档

| 查阅内容 | 文档 |
| --- | --- |
| CLI 职责、fixture、execution 和材料 | [工具设计](docs/design.md#命令与数据边界) |
| Go API 与执行报告 | [Go 库 API 规格](docs/library-entry-spec.md) |
| SDK 用户故事与 case | [共同验收](docs/cases.md) |
| 录制材料的身份、来源和可用状态 | [固定索引](data/index.json)、[内置快照](data/builtin/manifest.json) |
| 模块、测试层级和编码规则 | [开发规范](docs/development.md) |
| 文档术语 | [术语表](GLOSSARY.md) |
