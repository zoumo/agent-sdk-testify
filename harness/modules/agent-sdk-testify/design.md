---
type: Design
title: agent-sdk-testify 测试工具
status: stable
---

# agent-sdk-testify 测试工具

`agent-sdk-testify` 是单一 Go 二进制，提供 `record`、`verify`、`mock` 三个
公开业务入口。工具拥有 case、官方 driver、录制材料、索引和协议匹配；
消费者的 SDK 调用、业务断言、仿真编排和覆盖门禁由消费者维护。

生产代码和默认测试不导入任何 SDK 实现。官方 SDK 只在 `record`、`verify`
的显式环境中使用，工具不在运行时安装依赖或回落到真实 Agent。

| 模块 | 职责 |
| --- | --- |
| CLI 与运行器 | 解析命令、准备隔离环境、选择 case/driver、管理进程和汇总结果 |
| 官方 driver | 通过官方 SDK 执行一次 case，并给出业务结果 |
| 协议与回放 | stdio 采集、严格匹配、关联 ID 校验、完整消费、EOF 和回执 |
| 材料管理 | 资格校验、私有归档、采用、固定索引和内置快照 |
| mock | 用材料替代 Agent，提供查询、prepare、run 和 finish 模式 |

## 进程合同

### record

`record` 选择一个 provider/case，准备私有 workspace、HOME、临时目录和 provider
配置目录，启动官方 driver；driver 通过官方 SDK 对接调用方显式提供的真实 Agent。
录制代理保存双向原始帧、stderr、plan、来源 manifest、业务报告以及采集和清理结果。

失败、行为未触发和不支持的尝试同样保留原始证据。录制完成只证明本次采集范围，
不自动执行 `verify`、资格判断或采用。

### verify

`verify` 从录制身份确定 provider/case，选择对应官方 driver，并用严格回放引擎
替代真实 Agent。验证输出必须与原件分离，保留业务报告、每个选中连接的协议和退出
回执、失败诊断及 `verification.json`。

通过须同时满足：

- 原始文件、plan、manifest 和来源哈希有效。
- 官方 driver 的业务断言通过。
- 所有声明连接严格匹配并完整消费。
- 客户端 EOF、进程退出和工具自有资源清理成功。

验证证据绑定原件、合同、工具和 driver 哈希，并记录实际安装的官方 SDK 身份。
`--index` 可在验证成功后执行资格校验、私有归档和原子采用；验证本身不改写原件。

### mock

直接模式把录制当作原生 Agent executable：

```sh
agent-sdk-testify mock --provider codex --case SDK-CASE-001 --report /reports/direct.json
agent-sdk-testify mock --recording /captures/attempt --report /reports/direct.json
```

需要父进程环境、初始文件、多连接或独立收尾时，使用配套模式：

```sh
agent-sdk-testify mock --list [--provider PROVIDER] --json
agent-sdk-testify mock --describe --provider PROVIDER --case CASE --json
agent-sdk-testify mock --prepare --provider PROVIDER --case CASE --output /reports/run
agent-sdk-testify mock --run /reports/run/run.json [--connection main|probe]
agent-sdk-testify mock --finish --run /reports/run/run.json
```

`list` 报告全部 case 身份、覆盖要求和可用性；`describe` 返回材料身份、合同、
断言输入和兼容性；`prepare` 创建自有资源并写出 `run.json`；`run` 启动一个声明
连接；`finish` 核验全部连接并清理本次运行资源。

查询和收尾输出 JSON，`run` 的 stdout 只承载原生协议，诊断使用 stderr。
字段和错误语义以 [mock 运行合同](../../../docs/mock-run.md) 和
[catalog schema](../../../docs/mock-catalog.schema.json) 为准。

<a id="story-场景配置"></a>

## Case 与 provider 配置

[场景目录](../../../scenarios)保存用户故事、case、输入、预期和 provider 绑定。
case 身份、输入顺序、required/optional、`live-selected-path`、`planned` 和
`unsupported` 都是权威数据；人工构造的验证输入不能替代真实使用流程。

官方 driver 接收一个请求文件，至少包含 `case`、`materials`、`workspace`、
`peer` 和 `output`。driver 负责原生调用和业务断言，在输出目录写入
`result.json`；状态为 `passed`、`failed`、`untriggered` 或 `unsupported`。
运行器负责环境、进程、超时、取消和协议回执，不解释 SDK 业务事件。

新增 case 同时更新场景、provider binding、规范、测试和固定索引要求；不能只增加
一条成功录制或降低现有 required 集合。

## 环境与隔离

每次运行创建独立 workspace、HOME、tmp 和可写配置副本，只释放 case 声明的文件。
Codex 使用 `CODEX_HOME`；Claude 使用 `CLAUDE_CONFIG_DIR`。认证仅来自调用方显式
提供的配置，不复制个人 skills、插件、记忆、项目指令或无关会话。

`mock --prepare` 返回绝对路径、环境变量和连接命令。消费者必须在启动自己的 SDK
宿主前应用这些值；只设置 mock 子进程环境不能配置父进程的历史读取。

Codex 的原生项目 cwd 保持录制身份，可写 fixture 使用新的 workspace。Claude 在
新目录键下派生历史副本，只替换必要的顶层 cwd/project 身份，保留 ID、未知字段及
数字字面量。`materialization.json` 记录来源与派生路径及双向哈希；派生副本不能
冒充原件。

<a id="story-数据格式与存储"></a>

## 录制与材料

一次采集按以下结构保存：

```text
recordings/<provider>/<story>/<case>/<attempt>/
  plan.json
  manifest.json
  raw/raw.jsonl
  report.json
  official-replay/
  review.json
  adoption.json
  materialization.json
```

失败或未采用尝试保存在 Git 外私有归档。跟踪材料只包含允许发布的必要文件和
逐文件哈希；原始 plan/raw、attempt 身份及来源关联不可改写。

材料流转为：

```text
record → verify → qualification → adoption → recordings/index.json → builtin
```

- `qualification` 校验来源、隔离、官方业务结果、完整性和哈希。
- `adoption` 将完整原件归档，再原子写入采用目录和索引；失败时保留旧版本。
- `recordings/index.json` 明确列出每个 provider/case 的 adoption、verification、
  requirement、原因和缺口，不隐藏失败。
- `builtin/` 只选择 `adoption=admitted` 且 `verification=passed` 的材料，通过
  `builtin/manifest.json` 绑定 material、合同、来源和逐文件哈希。
- 二进制嵌入 `builtin/materials.tar.gz` 和 manifest，运行时不访问源码目录或网络。

生成器校验完整索引、采用记录、review、官方证据和文件闭包；缺文件、哈希漂移、
非 passed 或空选择均失败。归档和清单排序稳定，不因生成时间产生变化。
采用或公开范围不明确时保持失败，不能通过改写 raw、忽略字段或放宽匹配修正。

## 严格回放

录制使用 stdio JSONL v1。每帧保留 `ordinal`、`direction`、`channel`、`epoch`、
`elapsedMs` 和原始 `wire` JSON；`wire` 不从业务对象重建，未知字段、键顺序和
数字字面量保持不变。

回放按保存顺序发送服务端帧，以客户端请求作为对应输出的屏障。工具比较完整客户端
消息，只按协议 Adapter 的明确规则重绑定关联 ID。以下情况必须失败：

- 请求内容、顺序、连接或协议版本不匹配。
- 等待超时、客户端提前 EOF 或存在未消费的服务端帧。
- 来源、plan、manifest 或 raw 哈希无效。
- 原生进程异常退出或工具自有资源未清理。

mock 不执行录制中的 shell、文件编辑、hooks 或模型调用，也不重建其副作用。
不支持的分支或协议明确失败，不搜索其他录制、不猜测 provider、不启动真实 Agent。

## 生命周期与证据

`mock --prepare` 创建带所有权标记的临时根、workspace、历史和连接锁；
每个连接只能启动一次。运行器等待协议子进程并原子提交协议回执和进程回执。

消费者先关闭自己的 Session/Runtime 和宿主进程，再执行 `finish`；即使业务断言失败
也必须收尾。finish 核验每个声明连接的严格成功、完整消费、EOF、无原生启动、正常
退出及历史哈希，然后只删除工具拥有的临时根。占用、清理或哈希失败保留在报告中，
可以重试；工具不按进程名杀进程，也不删除调用方目录。

业务通过、协议通过、材料验证和发布资格是不同结论：

- `record` 只报告本次真实采集。
- `verify` 报告官方业务断言与严格回放。
- `mock` 只报告协议、退出和清理；消费者报告其 SDK 业务结果。
- SDK 仿真和产品 E2E 由消费者单独执行和报告。

默认检查全程离线，不启动真实 Agent、不新增录制，也不把合成测试或局部通过升级为
完整覆盖。

## 测试

测试穿过调用方实际使用的 CLI 和进程接口，使用本地受控子进程验证：

- JSONL 分片、大消息、未知字段和数字保真。
- 关联 ID 重绑定、错误匹配、提前 EOF、超时、取消和背压。
- 多连接独立回执、父进程环境、历史派生和 workspace 隔离。
- prepare/run/finish 的失败保留、重复收尾和自有资源清理。
- 从源码目录外运行发布二进制，以及材料闭包和内置快照一致性。

测试不得通过导入 SDK 来证明协议实现，也不得将人工合成材料登记为真实录制。
