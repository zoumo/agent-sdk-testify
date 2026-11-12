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

## Fixture 与命令

**Fixture（fixture / prepared fixture）**:
由 `prepare` 创建、由调用方管理和删除的隔离环境与持久数据根，使用
`--fixture-dir` 定位。一个 fixture 可承载多个 record/mock execution 和 verification。
它不是 Agent 原生 session，也不是 Agent 的 home 或原生配置目录。

**Native Agent session（原生 session）**:
Agent 或 SDK 自己定义和管理的会话。wire 字段 `session_id`、`SessionStart`、
`config/sessions` 和 `*.jsonl` 历史文件都保留这一原生含义，不属于工具 fixture。

**Initial source（初始来源）**:
`prepare` 选择的 `--case`、`--plan` 或 `--recording`。它决定 fixture 的初始材料和录制
环境，不限制后续可调用的业务动作。

**prepare**:
创建 fixture，校验来源，建立隔离环境，并把 case、plan 或已有录制材料完整复制到
fixture。它是唯一提供 source 选择的命令，不启动客户端、SDK、driver 或真实 Agent。

**Tool source（工具来源）**:
prepare 记录的工具入口身份：`executable` 表示 installed CLI，`embedded` 表示可导入
Go 库。两者使用判别式身份：executable 持久化版本与哈希、运行时解析当前可执行路径，embedded 记录模块路径、
版本/修订、可用时的提交时间和 Go 版本，不把 module path 当作 command，也不伪造
launcher。非 main 测试二进制缺少 module dependency 时，库调用方可用 exact pin 的
显式 `EmbeddedIdentity` 补充；能自证的 build info 始终优先。

**agent record**:
在 fixture 中显式启动真实 Agent，按 case 或 consumer plan 采集新的原生交互尝试。
成功后生成新 material 并原子替换 current recording；提交前失败保留 execution 证据但不
替换 current；提交后交付失败保留已提交事实，发布边界见工具设计。

**Official driver（官方驱动）**:
使用本仓库指定的官方 SDK 执行 case 的程序；负责业务操作、断言和 SDK 收尾。

**official record**:
编排官方 driver，通过公开 agent record 采集真实 Agent 交互，记录业务事实和
实际采集结果的关联；不拥有协议 execution。

**official verify**:
在 fixture 中驱动官方 driver 并记录验证事实；driver 调用公开 agent mock。official verify 核对
业务报告及实际 mock execution 的协议、EOF、退出和清理证据，绑定精确 material
哈希，不修改 current，也不拥有 execution 或连接；默认只产出验证证据。

**agent mock**:
在 fixture 中用 current recording 替代真实 Agent，作为消费者测试使用的原生协议
对端。mock 不要求 current 已经 verify，也不提供 Agent 版本或元命令。

**export**:
把 current recording 中封闭的 material core 原字节复制到 fixture 外；导出根就是 material 根。export 不改变 fixture，不导出
全部 execution 或 receipt，也不产生 verified 或入仓结论。

**Consumer（消费者）**:
通过公开 Go 库入口或 installed CLI/mock 的进程接口运行自身 SDK 或产品测试的一方。
业务断言和测试编排归消费者。

**Consumer capture（消费者录制）**:
外部消费者通过 `prepare --plan` 创建 fixture，由自己的 SDK 宿主启动 agent record，
代理真实 Agent 交互并保存的录制。工具准备隔离环境、配置和材料；它不是官方 case 录制。

**Action execution（action 执行）**:
fixture 中的一次 record 或 mock 执行，由
`packages/<materialId>/executions/<executionId>/execution.json` 描述；每个 execution 使用独立可写 overlay。
它不是额外的 CLI 步骤。

**Connection status（连接状态）**:
一个 connection 在 action execution 中的持久状态，按 `pending`、`running`、
`passed`、`failed` 进展。它只说明该连接，不代表同一 execution 的其他连接。

**Execution status（执行状态）**:
一个 action execution 全部声明连接的汇总状态。仍有 `pending` 或 `running` 连接时为
`open`，全部通过时为 `passed`，任一失败后为 `failed`。

**Action result（action 结果）**:
record 或 mock 返回的连接与 execution 状态、终态标志、材料身份和证据路径。连接
`passed` 不等于 `ExecutionComplete`；持久事实以 execution index 为准，无法刷新时
状态为 `unknown`。

**Execution start（执行启动快照）**:
`StartMockAgent` 在领取 execution、claim connection 并写入 running 结果后返回的只读
快照，包含实际 overlay 的 workspace、protocol cwd、home、config、tmp、有效环境、
native 声明和 connection 身份。它不是 prepare 的 route template，也不能由调用方
修改后冒充本次执行环境。

**Full consumption（完整消费）**:
服务端录制帧和已到达请求完成 pending request 检查及必要握手 barrier 后、额外客户端
字节和 EOF 检查前触发的唯一事件。它只允许 SDK 半关闭输入，不表示 EOF、清理或
execution 成功；完整消费到达前失败不触发，且不能替代 `Wait`。

**In-process mock（进程内 mock）**:
通过 `StartMockAgent` 和调用方提供的 `StartMockAgentStreams` 在同一进程内执行
严格回放，不启动 `agent-sdk-testify` 可执行文件。它仍创建正常 execution、receipt
和 report，但不具有进程、PID 或原生 CLI 退出码。

**Execution report（执行报告）**:
由 testify 从 execution descriptor、index、receipt 和 result 校验后生成的 typed
报告，覆盖 execution 声明的全部 connection。它区分本连接与整个 execution 状态，
进程内连接的 transport、EOF、清理和失败类别不依赖解析内部 JSON 文件。

**Record source（录制来源）**:
prepare 固化的 plan、协议项目和来源身份。每次 agent record 使用同一 record source，不随
current recording 的切换改变。

**Current recording（当前录制）**:
fixture 当前选中的唯一 materialId 及封存 core 哈希；由它定位 recording package。verify、mock 和 export 都以它为输入；record
成功提交时原子替换；提交前失败保持不变，提交后交付错误不撤销已提交事实。

## 材料与发布

**Capture attempt（采集尝试）**:
某个 agent type 对一个 case 的一次实际录制，包含 plan、raw、业务报告和收尾证据。

**Recording package（录制证据包）**:
以唯一 materialId 标识、由 fixture 与最终 case 共用布局的持久包，包含 material core
及独立 execution、record、verify 证据域；没有同值 recordingId 或 packageId 别名。

**Material（材料 / material core）**:
录制证据包内完整、不可变的回放输入，包含唯一派生 plan、原始 source plan、manifest、raw、绑定历史和来源闭包。Material 可以
直接作为 mock 或 export 的输入，不要求先通过 verify。

**Consumer material（消费者材料）**:
由 plan source 的 record 生成的外部材料。它可以通过同一 fixture 的 mock 消费或由
export 保存，但不经过官方验证或入仓资格，也不进入默认 case 库或内置快照。

**Verification（验证证据）**:
对精确 material 执行官方 verify 的结果，绑定 material 哈希、case/binding 身份、
工具、driver 和实际 SDK 身份，以及本次 mock execution/连接的精确引用。
它拥有验证事实与结论，不拥有协议 execution 或连接；不是 material 的类型，不能
转移到其他材料。

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
