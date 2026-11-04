# agent-sdk-testify 术语

本表定义本工具、录制材料和 mock 消费者共用的核心术语。相同名称只表示同一概念。

## Case 与协议

**User story（用户故事）**:
一个完整的 SDK 使用目标，由若干可独立录制和验证的 case 共同验收。

**Validation case（验证 case）**:
归属于一个用户故事、具有独立身份和输入顺序的完整使用流程。
_Avoid_: 单条断言、专项故障测试

**Agent type（agent 类型）**:
`codex` 或 `claude` 等具体原生 Agent。CLI 使用 `--agent` 表示，源能唯一确定时可自动
推断。

**Agent binding（agent 绑定）**:
某个 agent type 对 case 的原生参数、执行路径、required/optional 和限制。

**Connection（连接）**:
一次录制中的独立原生协议通道，例如 `main` 或远端流程的 `probe`。

**Strict replay（严格回放）**:
按录制顺序校验客户端和服务端原生帧，要求关联 ID、未知字段、完整消费和 EOF 一致。

**Receipt（回执）**:
一个连接或进程的持久结果，记录协议状态、退出、清理和失败诊断。

## 会话与命令

**Session（会话，也称 prepared 会话）**:
由 `prepare` 创建、由 `finish` 关闭的隔离环境与持久数据根，使用
`--session-dir` 定位。一次会话可承载多个 record、verify 和 mock execution。
它不是 Agent 的 home 或原生配置目录。

**Initial source（初始来源）**:
`prepare` 选择的 `--case`、`--plan` 或 `--recording`。它决定会话的初始材料和录制
环境，不限制后续可调用的业务动作。

**prepare**:
创建 session，校验来源，建立隔离环境，并把 case、plan 或已有录制材料完整复制到
session。它是唯一提供 source 选择的命令，不启动客户端、SDK、driver 或真实 Agent。

**record**:
在 session 中显式启动真实 Agent，按 case 或 consumer plan 采集新的原生交互尝试。
成功后生成新 material 并原子替换 current recording；失败保留 execution 证据但不
替换 current。

**verify**:
在 session 中评价 current recording，使用官方 driver 离线执行录制，并校验业务
断言、协议匹配、完整消费、EOF 和清理。verify 绑定精确 material 哈希，不修改
current recording；默认只产出验证证据。

**mock**:
在 session 中用 current recording 替代真实 Agent，作为消费者测试使用的原生协议
对端。mock 不要求 current 已经 verify，也不提供 Agent 版本或元命令。

**export**:
把 current recording 的完整材料包复制到 session 外。export 不改变 session，不导出
全部 execution 或 receipt，也不产生 verified 或入仓结论。

**finish**:
关闭 session，汇总全部 execution 的证据、历史哈希和清理状态，删除工具拥有的临时
运行环境；不删除 session 中的材料，也不隐式执行 verify、mock、export 或采用。

**Consumer（消费者）**:
通过 mock 的进程接口运行自身 SDK 或产品测试的一方。业务断言和测试编排归消费者。

**Consumer capture（消费者录制）**:
外部消费者通过 `prepare --plan` 创建会话，再由 record 用自身 SDK 或运行时驱动真实
Agent 并保存的原生交互录制。工具准备隔离环境、配置和材料；它不是官方 case 录制。

**Action execution（action 执行）**:
会话中的一次 record、verify 或 mock 执行，由
`executions/<executionId>/execution.json` 描述；每个 execution 使用独立可写 overlay。
它不是额外的 CLI 步骤。

**Record source（录制来源）**:
prepare 固化的 plan、执行器和来源身份。每次 record 使用同一 record source，不随
current recording 的切换改变。

**Current recording（当前录制）**:
session 当前选中的唯一 material。verify、mock 和 export 都以它为输入；record
成功时原子替换，失败时保持不变。

## 材料与发布

**Capture attempt（采集尝试）**:
某个 agent type 对一个 case 的一次实际录制，包含 plan、raw、业务报告和收尾证据。

**Material（材料）**:
完整、不可变的录制包，包含 plan、manifest、raw、绑定历史和来源哈希。Material 可以
直接作为 mock 或 export 的输入，不要求先通过 verify。

**Consumer material（消费者材料）**:
由 plan source 的 record 生成的外部材料。它可以通过同一 session 的 mock 消费或由
export 保存，但不经过官方验证或入仓资格，也不进入默认 case 库或内置快照。

**Verification（验证证据）**:
对精确 material 执行官方 verify 的结果，绑定 material 哈希、case/binding 身份、
工具和 driver 身份。它不是 material 的类型，不能转移到其他材料。

**Admission eligibility（入仓资格）**:
精确官方 case material 同时具备官方业务验证、来源、隔离、材料闭包和哈希证据，
可以提交默认 case 库的状态。

**Admission（采用）**:
将具备入仓资格的材料显式归档并写入固定索引和默认材料库的原子动作。采用成功不代表
内容已获公开发布授权。

**Fixed index（固定索引）**:
`data/index.json`，列出每个 agentType/case 的材料、采用状态、验证结果和缺口。

**Builtin snapshot（内置快照）**:
从固定索引选择 `admitted` 且 `verification=passed` 的材料，压入二进制并由 manifest
哈希绑定。

**Simulation（仿真）**:
消费者用 mock 回放录制并执行自身业务测试的过程。仿真结果由消费者报告，工具只报告
协议是否完成。

**Synthetic material（合成材料）**:
人工构造、仅用于协议或错误路径测试的输入，不是录制证据，也不计入用户故事验收。

**Evidence level（证据层）**:
真实录制、官方 SDK 验证、SDK 仿真和产品 E2E 等独立结果层级；不同层级不能互相替代。
