# 从 SDK 测试使用 mock

`agent mock` 在 session 中把 current recording 作为原生 Agent 协议对端。SDK 仍调用自己的
公共接口；消费者负责业务断言、SDK 宿主和远端服务，工具负责严格匹配、完整消费、
EOF、退出和清理。mock 不要求材料已经 verify，不模拟模型调用或录制中的文件副作用。

生命周期与数据边界以[工具设计](design.md)为准。公开命令为 prepare、agent record/mock、official record/verify、
export、finish；只有 prepare 接受材料来源。mock 没有 list、describe、managed
alias、材料覆盖或 Agent 版本查询入口。

## 准备材料与会话

内置材料不需要源码、Python 或官方 SDK。外部材料使用完整录制包，也可由
[消费者录制](consumer-recording.md)产生。两种导入方式分别为：

```sh
agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
  --session-dir /reports/case-session

agent-sdk-testify prepare --recording /captures/attempt \
  --session-dir /reports/recording-session
```

后文使用 `/reports/recording-session`。session 必须是尚不存在的绝对目录，父目录已
存在，且来源不位于目标 session 内。prepare 完整复制输入和绑定历史，并校验副本；
之后删除外部原件不影响动作。没有内置材料的 case 或 consumer plan 可 prepare，
但 current 为空时 mock、verify、export 失败，不产生空成功。

材料身份和缺口由[固定索引](../data/index.json)、[内置快照](../data/builtin/manifest.json)
及[共同验收](cases.md)提供。可用子集不能代表完整 required 覆盖或 SDK 验收通过。

## SDK 宿主环境

prepare 在 session 中写入只读 `session.json`。它是内部 descriptor，不能 source、
eval 或修改；CLI 参数始终是目录，不是 descriptor 路径。

| 字段 | 用途 |
| --- | --- |
| `schemaVersion` | 当前为 `2`；不兼容版本必须重新 prepare |
| `sessionId` | 会话身份 |
| `tool.command`, `toolVersion`, `toolSHA256` | 工具路径与实际身份 |
| `source`（含 `sha256`）, `agentType` | 不可变初始来源 |
| `resourceRoot` | 工具拥有的临时运行根 |
| `routes` | 动作环境槽和连接模板；实际可执行性由动作前提检查决定 |
| route 的 `workspace`, `env` | SDK 宿主 cwd 和需要合并的环境 |
| route 的 `connections` | 本次动作的连接调用模板 |
| `finish.command`, `finish.args` | 收尾命令 |

启动 SDK 宿主前，应用当前 route 的 env 和 workspace；只设置 mock 子进程环境不能
配置父进程的历史 API。Codex 使用 protocolCwd 配置协议项目，Claude 使用 workspace
配置 WorkDir 和历史查询。

每个 record/mock execution 在 `executions/<executionId>/execution.json` 写入独立 descriptor，记录
本次输入 material、workspace、env、连接、receipt 和 limits；可写 overlay 位于临时
resourceRoot。环境基线与 session descriptor 不随 action 改写。

`materialization.json` 记录历史副本的来源、派生路径和双向哈希。Claude 只替换必要的
顶层 cwd/project 身份，保留 ID、未知字段和数字字面量。只有材料或 plan 明确绑定的
历史会被派生，不复制个人 skills、插件、指令或整个 Agent home。

## 启动连接

```sh
agent-sdk-testify agent mock --session-dir /reports/recording-session
agent-sdk-testify agent mock --session-dir /reports/recording-session --connection probe
```

mock 只消费 current，默认连接为 main。只有材料确实声明多个独立客户端生命周期时
才能使用 probe 等连接；各连接可以并发，每条连接最多启动一次。普通单连接动作结束
后，再次 mock 会创建新的 execution。会话执行与验证的协调规则见
[工具设计](design.md#多个-action-execution)。

| 连接字段 | 用途 |
| --- | --- |
| `id` | main 或材料声明的其他连接 |
| `command`, `args` | 可直接执行的原生协议命令 |
| `launcher` | 只接受 executable 的 SDK 使用的临时启动器 |
| `recording` | 当前连接的录制 |
| `planSHA256`, `manifestSHA256`, `recordingSHA256` | 材料身份 |
| `receipt`, `processReceipt` | 持久协议回执和进程回执 |

launcher 将追加的原生启动参数传给 mock。参数必须通过协议白名单，不能覆盖 session、
连接或运行参数；版本、帮助等元命令被拒绝。SDK 若自行探测原生 CLI 版本，需要在其
测试配置中处理，mock 不提供该应答。工具顶层 --version 只报告工具版本。

消费者根据明确的 case 和材料输入编写业务断言；协议帧和材料来源字节保持不变。
不得改写录制、忽略未知字段、另写宽松 matcher，或把捕获帧直接发回 mock 作为 SDK
业务通过的证据。mass/2 等客户端与录制握手不一致时应失败。

## 导出与复用

```sh
agent-sdk-testify export --session-dir /reports/recording-session \
  --output /captures/exported-recording
agent-sdk-testify prepare --recording /captures/exported-recording \
  --session-dir /reports/imported-session
```

目标必须位于 session 外、父目录存在且目标尚不存在。export 只复制 current 的完整
材料包，不包含全部 execution、receipt 或 verification，不改变 session 或可信等级。
可以导出到多个新目标；导出包可再次 prepare。需要保留某次 current 时，应在下一次
record 替换它之前显式导出；action 不接受旧材料选择参数。

## 关闭与收尾

先关闭消费者自己的 Session/Runtime，等待全部 peer 退出，再执行：

```sh
agent-sdk-testify finish --session-dir /reports/recording-session
```

finish 检查活动 execution 和 official 编排。忙碌时不写状态、不清理；取得收尾权后立即 closed，禁止新
execution。它持久保存每个 execution 的核验结论，再清理工具拥有的临时根；不终止
消费者进程，不删除 session、material、execution 或调用方文件，也不隐式执行业务
动作、导出或采用。

清理失败不重新打开会话。再次 finish 只重试未完成清理，使用已保存的业务结论，不因
修复或改动 receipt 而重算成功。任何失败不能被其他成功 execution 抵消。closed 后
仍能 export；已清理完成的 finish 可重复调用。

## 持久结果

session 中保留：

- `session.json` 和 `source/`：会话与不可变 record source。
- `materials/index.json`、`materials/<materialId>/`：不可变材料注册表和完整闭包。
- `current.json`：唯一 current material 身份，或没有 current。
- `executions/index.json`：execution 与连接状态索引。
- `executions/<executionId>/execution.json`：record/mock execution 的 descriptor。
- execution 中的 `receipts/`：协议、EOF、进程退出和错误证据。
- execution 中的 `record/`：采集或失败证据。
- `official-records/<runId>/`：官方录制业务报告和实际采集结果引用。
- `verifications/<verificationId>/`：driver 报告、失败诊断及引用 mock execution 的验证证据。
- execution 中的 `finish.json`：首次收尾的逐 execution 核验结论。
- `finish.json`、`cleanup.json`、`closed`：最终结论、清理进度和关闭标记。
- `prepare.json`：prepare 的结果或失败诊断。

策略比较可在同一会话使用不同 mock execution，也可分别 prepare 会话。业务断言、
SDK 仿真、当前兼容性和完整覆盖由消费者分别报告，协议与清理通过不能替代它们。
