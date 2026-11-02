# agent-sdk-testify 工具设计

`agent-sdk-testify` 是面向多语言 SDK 的独立测试工具，公开业务入口只有
`record`、`verify` 和 `mock`。工具维护 case、官方 driver、录制材料、固定索引与
内置快照；消费方的业务测试编排和 SDK 仿真归消费方自己维护。

核心边界见下表；仓库路径、编码和测试规则见[开发规范](development.md)。

## 核心边界

| 命令 | 职责 | 执行方与对端 | 输出结论 |
| --- | --- | --- | --- |
| `record` | 按本仓库 case 录制真实 Agent 交互 | 官方 driver → 官方 SDK → 录制代理 → 真实 Agent | 业务执行、采集完整性、退出和清理结果 |
| `verify` | 用官方 SDK 验证已有录制 | 官方 driver → 官方 SDK → 严格回放引擎 | 官方业务断言、协议匹配、完整消费、EOF 和清理是否通过 |
| `mock` | 使用录制充当 Agent | 消费者自己的测试 → 被测 SDK → mock | 每个连接的协议匹配、完整消费、EOF、退出与清理结果 |

`simulate`、Go driver 和 SDK 覆盖门禁不属于本工具。SDK 或 MASS 通过进程接口消费
`mock`，工具不替消费者执行其业务断言。

| 模块 | 职责 |
| --- | --- |
| CLI 与运行器 | 解析命令、准备隔离环境、选择 case/driver、管理进程和汇总结果 |
| 官方 driver | 通过官方 SDK 执行一次 case，并给出业务结果 |
| 协议与回放 | stdio 采集、严格匹配、关联 ID 校验、完整消费、EOF 和回执 |
| 材料管理 | 资格校验、私有归档、采用、固定索引和内置快照 |
| mock | 用材料替代 Agent，提供查询、prepare、run 和 finish 模式 |

生产代码和默认测试不导入任何 SDK 实现。官方 SDK 只是 `record`、`verify` 的显式
环境依赖，不在运行时自动安装。

## 命令合同

### record

```sh
agent-sdk-testify record --provider codex --case SDK-CASE-008 \
  --cli /absolute/codex --config /absolute/selected-config \
  --output /private/new-capture
```

工具选择 case 和 provider binding，准备隔离环境，调用官方 SDK 对接真实 Agent，
保存原始双向帧、stderr、plan、来源 manifest、业务报告、采集和清理结果。

只有 `record` 可以连接真实 Agent，且必须由操作者显式执行。失败、未触发和不支持
的尝试同样保留原始证据。录制成功只代表本次采集达到其业务、采集与清理要求，仍需
`verify`；不自动执行资格判断或采用。

### verify

```sh
agent-sdk-testify verify --recording /private/new-capture \
  --output /private/new-verification --timeout 2m
```

provider/case 从录制身份确定，工具只选择对应的官方 driver。对端使用严格回放引擎，
不连接真实 Agent。通过须同时满足：

- 原始文件、plan、manifest 和来源哈希有效。
- 官方 driver 的业务断言通过。
- 所有声明连接严格匹配并完整消费。
- 客户端 EOF、进程退出和工具自有资源清理成功。

验证不改写原始录制。验证证据与原件分开保存，并绑定录制、合同、工具和 driver 哈希，
同时记录实际安装的官方 SDK 包及版本。`--index` 可在验证成功后执行资格校验、私有
归档和原子采用；采用失败保留验证证据和旧索引/材料。

### mock

普通场景可直接替换 Agent executable：

```sh
agent-sdk-testify mock --provider codex --case SDK-CASE-008 \
  --report /reports/mock-001.json
agent-sdk-testify mock --recording /captures/attempt \
  --report /reports/mock-001.json
```

需要父进程环境、初始文件或多连接时，使用 `list`、`describe`、`prepare`、`run` 和
`finish` 模式。默认启动模式的 stdout 只承载原生协议；查询与收尾输出结构化结果，
诊断写入 stderr 或报告文件。命令、描述符和错误语义见
[mock 使用指南](using-mock.md)。

## Case 与 provider 配置

[场景目录](../data/scenarios)保存用户故事、case、输入、预期和 provider 绑定。
case 身份、输入顺序、required/optional、`live-selected-path`、`planned` 和
`unsupported` 都是权威数据；人工构造的验证输入不能替代真实使用流程。

官方 driver 接收一个请求并写入 `result.json`；请求与结果字段见
[driver 文档](../drivers/README.md#单请求协议)。运行器负责环境、进程、超时、取消
和协议回执，不解释 SDK 业务事件。

新增 case 同时更新场景、provider binding、验收规范、测试和固定索引要求；不能只
增加一条成功录制或降低现有 required 集合。全部用户目标和通过边界见
[共同验收](cases.md)。

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

## 录制、材料与发布

一次采集按以下结构保存：

```text
data/<provider>/<story>/<case>/<attempt>/
  plan.json
  manifest.json
  raw/raw.jsonl
  report.json
  official-replay/
  review.json
  adoption.json
  materialization.json
```

材料链路：

```text
record → verify → qualification → adoption → data/index.json → data/builtin → mock
```

- 完整原始采集包和失败尝试保存在 Git 外私有归档，保留成功、失败、原件身份和哈希。
- `qualification` 校验来源、隔离、官方业务结果、完整性和哈希。
- `adoption` 将完整原件归档，再原子写入采用目录和 `data/index.json`；索引保留所有
  requirement、verification、失败和缺口。
- `data/builtin/` 只选择 `adoption=admitted` 且 `verification=passed` 的材料，通过
  manifest 绑定 material、合同、来源和逐文件哈希。
- 二进制嵌入内置 tar 包和 manifest，运行时不访问源码目录或网络。

原始 plan/raw、attempt 身份及来源关联不可改写。构建拒绝材料缺失、哈希漂移、验证
未通过或空选择；采用成功不单独证明材料可以公开发布。内置材料的生成和校验见
[开发规范](development.md#材料与测试输入)，查询和断言输入见
[mock 使用指南](using-mock.md)。

## 严格回放

录制使用 stdio JSONL v1。每帧保留 `ordinal`、`direction`、`channel`、`epoch`、
`elapsedMs` 和原始 `wire` JSON；`wire` 不从业务对象重建，未知字段、键顺序和数字
字面量保持不变。

回放按保存顺序发送服务端帧，以客户端请求作为对应输出的屏障。工具比较完整客户端
消息，只按协议 Adapter 的明确规则重绑定关联 ID。以下情况必须失败：

- 请求内容、顺序、连接或协议版本不匹配。
- 等待超时、客户端提前 EOF 或存在未消费的服务端帧。
- 来源、plan、manifest 或 raw 哈希无效。
- 原生进程异常退出或工具自有资源未清理。

mock 不执行录制中的 shell、文件编辑、hooks 或模型调用，也不重建其副作用。
不支持的分支或协议明确失败，不搜索其他录制、不猜测 provider、不启动真实 Agent。

## 生命周期与证据

`mock --prepare` 创建带所有权标记的临时根、workspace、历史和连接锁；每个连接只能
启动一次。消费者先关闭自己的 Session/Runtime 和宿主进程，再执行 `finish`；即使
业务断言失败也必须收尾。

finish 核验每个声明连接的严格成功、完整消费、EOF、无原生启动、正常退出及历史哈希，
然后只删除工具拥有的临时根。占用、清理或哈希失败保留在报告中，可以重试；工具不按
进程名杀进程，也不删除调用方目录。完整描述、回执和清理语义见
[mock 使用指南](using-mock.md)。

业务通过、协议通过、材料验证和发布资格是不同结论：

- `record` 只报告本次真实采集。
- `verify` 报告官方业务断言与严格回放。
- `mock` 只报告协议、退出和清理；消费者报告其 SDK 业务结果。
- SDK 仿真和产品 E2E 由消费者单独执行和报告。

默认检查全程离线，不启动真实 Agent、不新增录制，也不把合成测试或局部通过升级为
完整覆盖。当前材料状态、缺口和官方环境限制分别由
[data/index.json](../data/index.json)、
[data/builtin/manifest.json](../data/builtin/manifest.json) 和
[driver 环境说明](../drivers/README.md#官方-sdk-环境)提供。
