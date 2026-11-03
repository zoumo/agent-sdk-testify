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

## 命令

**prepare**:
按 `--case`、`--plan` 或 `--recording` 创建经过一次校验、可多次承载 action execution 的隔离
会话；准备环境基线、路由和材料，不启动客户端或真实 Agent。

**record**:
在 prepared 会话中显式启动真实 Agent，按 case 或 consumer plan 采集一个新的原生
交互 attempt，并把 capture 登记到会话。

**verify**:
在 prepared 会话中使用官方 SDK 离线执行录制，校验业务断言、协议匹配、完整消费、
EOF 和清理；默认选择会话中最新兼容 capture，但不消费它。

**mock**:
在 prepared 会话中用录制替代真实 Agent，作为消费者测试使用的原生协议对端；默认
选择最新兼容 capture 或会话初始材料。

**finish**:
结束 prepared 会话，汇总全部 execution 的业务证据、历史哈希和清理状态；不接收业务 action。

**Consumer（消费者）**:
通过 `mock` 的进程接口运行自身 SDK 或产品测试的一方。业务断言和测试编排归消费者。

**Consumer capture（消费者录制）**:
外部消费者通过 `prepare --plan` 创建会话，再由 record 用自身 SDK 或运行时驱动真实
Agent 并保存的原生交互录制。工具准备隔离环境、配置和材料；它不是官方 case 录制。

**Prepared session（prepared 会话）**:
由 `--session-dir DIR` 定位、经过 prepare 和 finish、可承载多个 action execution
的隔离会话目录；`session.json` 是其内部只读合同。它不是 Agent 的 home 或原生配置
目录。source、executor 和 target 在会话及 execution 上确定。

**Action execution（action 执行）**:
prepared 会话中的一次 record、verify 或 mock 执行，由
`executions/<executionId>/execution.json` 描述；每个 execution 使用独立可写 overlay。
它不是额外的 CLI 步骤。

**Capture handoff（录制交接）**:
record 把本次 capture 登记到会话，供后续 verify/mock 选择；默认选择最新兼容
capture，verify 不消费或改写它。

**Managed alias（托管别名）**:
`mock` 直接作为 Agent executable、无需显式会话的单连接兼容入口。它复用相同的
prepare、action、finish 状态机和证据语义，不是独立生命周期。

## 材料与发布

**Capture attempt（采集尝试）**:
某个 agent type 对一个 case 的一次实际录制，包含 plan、raw、业务报告和收尾证据。

**Material（材料）**:
一次经过审核采用的 capture attempt，可作为 mock 的输入。

**Consumer material（消费者材料）**:
由 plan source 的 record 生成、按路径交给同一会话或 `mock --recording` 的 external 材料。
它不经过官方资格、采用或内置快照，结论为 `qualification: unqualified`。

**Adoption（采用）**:
将验证通过且有来源证据的尝试写入固定录制库的内部动作。
状态为 `admitted` 时，不代表内容已获公开发布授权。

**Fixed index（固定索引）**:
`data/index.json`，列出每个 agentType/case 的材料、采用状态、验证结果和缺口。

**Builtin snapshot（内置快照）**:
从固定索引选择 `admitted` 且 `verification=passed` 的材料，压入二进制并由 manifest 哈希绑定。

**Qualification（资格）**:
材料是否具备来源、隔离、官方业务和完整性证据的内部判断。

**Simulation（仿真）**:
消费者用 `mock` 回放录制并执行自身业务测试的过程。仿真结果由消费者报告，工具只报告协议是否完成。

**Synthetic material（合成材料）**:
人工构造、仅用于协议或错误路径测试的输入，不是录制证据，也不计入用户故事验收。

**Evidence level（证据层）**:
真实录制、官方 SDK 验证、SDK 仿真和产品 E2E 等独立结果层级；不同层级不能互相替代。
