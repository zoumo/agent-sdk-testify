# 从 SDK 测试使用 mock

`agent mock` 在 fixture 中把 current recording 作为原生 Agent 协议对端。SDK 仍调用自己的
公共接口；消费者负责业务断言、SDK 宿主和远端服务，工具负责严格匹配、完整消费、
EOF、退出和清理。mock 不要求材料已经 verify，不模拟模型调用或录制中的文件副作用。

生命周期与数据边界以[工具设计](design.md)为准。公开命令为 prepare、agent record/mock、official record/verify、
export；只有 prepare 接受材料来源。mock 没有 list、describe、managed
alias、材料覆盖或 Agent 版本查询入口。

## 准备材料与 Fixture

内置材料不需要源码、Python 或官方 SDK。外部材料使用完整录制包，也可由
[消费者录制](consumer-recording.md)产生。两种导入方式分别为：

```sh
agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
  --fixture-dir /reports/case-fixture

agent-sdk-testify prepare --recording /captures/attempt \
  --fixture-dir /reports/recording-fixture
```

后文使用 `/reports/recording-fixture`。fixture 必须是尚不存在的绝对目录，父目录已
存在，且来源不位于目标 fixture 内。prepare 按[导入边界](design.md#导入边界)识别完整新包、standalone core 或 legacy 包，
完整复制并校验；
之后删除外部原件不影响动作。没有内置材料的 case 或 consumer plan 可 prepare，
但 current 为空时 mock、verify、export 失败，不产生空成功。

材料身份和缺口由[固定索引](../data/index.json)、[内置快照](../data/builtin/manifest.json)
及[共同验收](cases.md)提供。可用子集不能代表完整 required 覆盖或 SDK 验收通过。

## SDK 宿主环境

prepare 在 fixture 中写入只读 `fixture.json`。它是内部 descriptor，不能 source、
eval 或修改；CLI 参数始终是目录，不是 descriptor 路径。

| 字段 | 用途 |
| --- | --- |
| `schemaVersion` | 当前为 `7`；schema 6 及更旧版本必须重新 prepare |
| `fixtureId` | fixture 身份 |
| `tool.source` | `executable` 或 `embedded` 工具身份判别式 |
| `tool.version` | 工具来源的精确版本 |
| `tool.sha256` | executable 工具哈希；本次 command 在运行时解析并校验，不持久化本机路径 |
| `tool.module`, `tool.moduleSum`, `tool.moduleRevision`, `tool.moduleTime`, `tool.goVersion` | embedded 工具的模块来源身份；`moduleTime` 是可用时的 VCS 提交时间 |
| `source`（含 `sha256`）, `agentType` | 不可变初始来源 |
| `routes` | 稳定动作能力与连接合同；本机路径、命令和环境值不在此持久化 |

fixture control 使用 schema 7，`current.json` 只选择 material ID 对应的 package。runtime
根固定为 `<fixture>/runtime`，workspace、HOME、config、tmp、launcher 和命令参数
由当前 fixture 根、material ID、execution ID 与 action 在启动时构造，不再作为
可持久化本机路径写入 descriptor。package 内证据使用 package 根相对引用，
material 内部引用使用 material 根相对引用。

启动 SDK 宿主前，Go 调用方使用 prepare/Open 的公开运行时对象，CLI 脚本读取
`runtime/routes/mock.json`；进程内 mock 使用本次 execution 的 `ExecutionStart`。
读取和过期检查见[SDK 宿主的运行时入口](design.md#sdk-宿主的运行时入口)。
这些入口提供已经解析的 env、workspace 和连接命令；这些值由当前根计算，
不是 `fixture.json` 中的持久字段。只设置 mock 子进程环境不能配置父进程的历史 API。
Codex 使用 protocolCwd 配置协议项目，Claude 使用 workspace 配置 WorkDir 和历史查询。
公开 Go 运行时对象继续返回绝对路径；磁盘 descriptor 不保存这些值。

每个 record/mock execution 在
`packages/<materialId>/executions/<executionId>/execution.json` 写入独立
descriptor，记录本次输入 material ID、connection、limits 及 package 相对证据
引用；workspace、env 和 launcher 在运行时由 fixture 根和 ID 解析，不写入磁盘
descriptor。状态与后出的哈希保存在 execution/result.json。可写 overlay 位于临时 runtime。环境基线与 fixture descriptor 不随
action 改写。
普通 mock 和 official mock 使用同一 descriptor、receipt、isolation 和 finalize
合同；只有 official verify 另外在同一 package 新建 `verify/<verificationId>/`。

execution 的 `materialization.json` 记录历史来源、派生目标路径、源哈希和目标哈希；
`isolation.json` 另外记录有效 cwd、argv、env 关系和这些证据的哈希。两者是不同证据对象，不能用一个替代另一个。
materialization 的版本、源/派生字节与目标绑定见[材料计划与运行配置](design.md#材料计划与运行配置)。
fixture 的 `source/history-seed/` 只保留一份冻结 seed；material 使用
`material/history/seed/` 与 `material/history/runtime/` 的规范布局和派生 plan
historyBindings 表；原始 source plan 单独保留。Claude 只替换必要的顶层 cwd/project 身份，保留 ID、未知字段和数字
字面量。只有材料或 plan 明确绑定的历史会被派生，不复制个人 skills、插件、指令
或整个 Agent home。

## 启动连接

```sh
agent-sdk-testify agent mock --fixture-dir /reports/recording-fixture
agent-sdk-testify agent mock --fixture-dir /reports/recording-fixture --connection probe
```

mock 只消费 current，默认连接为 main。只有材料确实声明多个独立客户端生命周期时
才能使用 probe 等连接；各连接可以并发，每条连接最多启动一次。普通单连接动作结束
后，再次 mock 会创建新的 execution。fixture 执行与验证的协调规则见
[工具设计](design.md#多个-action-execution)。

运行时连接对象的字段如下；这些值由公开 Go 运行时对象或 CLI 的 runtime route 快照提供，
它们不是 `fixture.json` 的持久字段。

| 连接字段 | 用途 |
| --- | --- |
| `id` | main 或材料声明的其他连接 |
| `transport` | `process` 或 `in-process`；决定是否存在可执行字段 |
| `command`, `args` | 本次 process 连接可执行的原生协议命令 |
| `launcher` | 本次 process 连接为只接受 executable 的 SDK 生成的临时启动器 |
| `recording` | 当前连接的录制 |
| `materialId` | current package 与成功 material 的唯一 canonical 身份 |
| `planSHA256`, `manifestSHA256`, `recordingSHA256` | 材料身份 |
| `receipt`, `processReceipt` | 公开运行时对象给出当前绝对路径；CLI 通知与磁盘 result 使用 package 根相对引用 |

mock 进程 receipt 的字段合同见[工具设计](design.md#agent-mock)。

launcher 将追加的原生启动参数传给 mock。参数必须通过协议白名单，不能覆盖 fixture、
连接或运行参数；版本、帮助等元命令被拒绝。SDK 若自行探测原生 CLI 版本，需要在其
测试配置中处理，mock 不提供该应答。工具顶层 --version 只报告工具版本。

消费者根据明确的 case 和材料输入编写业务断言；协议帧和材料来源字节保持不变。
不得改写录制、忽略未知字段、另写宽松 matcher，或把捕获帧直接发回 mock 作为 SDK
业务通过的证据。mass/2 等客户端与录制握手不一致时应失败。

## 导出与复用

```sh
agent-sdk-testify export --fixture-dir /reports/recording-fixture \
  --output /captures/exported-recording
agent-sdk-testify prepare --recording /captures/exported-recording \
  --fixture-dir /reports/imported-fixture
```

目标必须位于 fixture 外、父目录存在且目标尚不存在。export 只复制 current 的完整
material core，导出根就是 material 根；不包含整个 package、execution、record、
receipt 或 verification，也不改变 fixture 或可信等级。可以导出到多个新目标；
导出包可再次 prepare 并装入新的 package 布局。需要保留某次 current material 时，
应在下一次 record 替换 current 之前显式导出；action 不接受旧材料选择参数。

## 释放 Fixture

关闭消费者自己的 SDK、Session/Runtime 并等待所有 peer 退出后，调用方直接删除
fixture 目录。需要保留录制时先 export；完成全部结算后只删除 `runtime/` 可以保留 package、
material 与执行证据。
目录边界与释放时序见[工具设计](design.md#fixture-目录所有权)。

## 持久结果

fixture 中保留：

- `fixture.json` 和 `source/`：schema 7 fixture control，以及唯一冻结 source seed。
- `current.json`：唯一 current material ID，或没有 current。
- `executions/index.json`：fixture control 的 execution 与连接调度索引，不进入
  final case。
- `packages/<materialId>/material/`：成功 record 封闭的不可变 material core，
  包含唯一 plan、manifest、raw、规范 history 和来源哈希。
- `packages/<materialId>/executions/<executionId>/`：record/mock execution 的
  descriptor、materialization、isolation、prepared plan 投影、receipts 和域结果。
- `packages/<materialId>/record/<runId>/`：official record 的业务报告、driver
  结果、stdout/stderr 和精确 execution 引用。
- `packages/<materialId>/verify/<verificationId>/`：official verify 的结论、
  driver 结果、replay report 和实际 mock execution 引用。
- `prepare.json`：prepare 的结果或失败诊断。

策略比较可在同一 fixture 使用不同 mock execution，也可分别 prepare fixture。业务断言、
SDK 仿真、当前兼容性和完整覆盖由消费者分别报告，协议与清理通过不能替代它们。
