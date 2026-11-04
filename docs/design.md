# agent-sdk-testify 工具设计

`agent-sdk-testify` 是面向多语言 SDK 的独立录制与回放工具。它维护 case、
官方 driver、录制材料、固定索引和内置快照；SDK 业务测试由消费者自己编排。

公开 CLI 入口为 `prepare`、`record`、`verify`、`mock`、`export` 和 `finish`。
`prepare`、`finish` 管理会话生命周期，`export` 只读取会话，`record`、`verify`、
`mock` 是三个互相独立的业务动作。仓库路径、编码和测试规则见
[开发规范](development.md)。

## 问题与目标

录制、验证、回放和消费者测试通常需要不同的隔离环境，但不应因此产生多套生命周期。
本工具用一次 `prepare` 建立一个自包含 session，之后在同一 session 中反复执行
`record`、`verify`、`mock`，直到显式 `finish`。

目标如下：

- `session-dir` 是唯一数据根。外部材料只在 prepare 时进入 session，后续动作不再
  读取原路径。
- 一次 prepare 可以承载多个独立 action execution。record 成功后产生的录制成为
  current recording，后续 verify 和 mock 默认消费它。
- record 负责产生录制，verify 负责评价录制，mock 负责回放录制。三者共享 session、
  execution、receipt 和最终证据语义。
- verified 只表示某个精确材料通过了验证，不是 mock 的前置条件，也不是材料进入仓库
  的充分条件。
- 外部消费者可以自行准备 plan、录制材料和业务验证；官方 case 仓库只接收满足官方
  验证和入仓条件的材料。

## 非目标

- 不编排消费者的 SDK 业务断言、产品 E2E 或仿真门禁。
- 不模拟 Agent CLI 的版本查询、帮助或其他元命令；mock 只提供协议回放。
- 不强制 `record → verify → mock` 顺序。三个动作是否可执行由各自前提决定。
- 不把“命令可以调用”解释为“命令必然成功”，也不在 source 类型上添加不必要的
  命令级禁令。
- 不让 finish、export 或 mock 隐式执行 verify、资格判断或仓库写入。
- 不降低严格回放要求，不为会话复用增加通配、忽略字段或自动生成关联 ID 的规则。

## 领域模型

| 概念 | 定义 |
| --- | --- |
| Session | 由 prepare 创建、由 finish 关闭的临时环境与持久数据根。 |
| Initial source | prepare 选择的 case、plan 或已有 recording；只决定初始状态，不限制后续动作。 |
| Material | 完整、不可变的录制材料包，包含 plan、manifest、raw、绑定历史和来源哈希。 |
| Current recording | session 当前选中的唯一 material；verify、mock 和 export 以它为输入，record 成功时替换它。 |
| Action execution | 一次 record、verify 或 mock 产生的 execution，具有独立 descriptor、overlay、receipt 和失败证据。 |
| Receipt | 连接或进程的持久结果，记录协议、退出、清理和诊断。 |
| Verification | 对精确 material 做官方 verify 后产生的证据，绑定 material 哈希和实际执行身份。 |
| Admission | 把具备资格的材料显式归档并写入默认 case 库的动作。 |
| Export | 把 current recording 的完整材料包复制到 session 外的只读导出动作。 |
| Finish | 关闭 session、汇总 execution 证据并清理工具拥有的临时运行环境的阶段。 |

Material 是回放输入，不必先经过 verify。Verified 是 material 上的一份证据，不是
material 的固有类型，也不会随着 current recording 切换而转移。

## 会话生命周期

规范流程是：

```sh
agent-sdk-testify prepare --case SDK-CASE-001 --session-dir /private/session
agent-sdk-testify record --session-dir /private/session \
  --cmd /absolute/agent -- <native-args>
agent-sdk-testify verify --session-dir /private/session
agent-sdk-testify mock --session-dir /private/session
agent-sdk-testify export --session-dir /private/session \
  --output /private/exported-recording
agent-sdk-testify finish --session-dir /private/session
```

`--session-dir` 在所有命令中表示同一个绝对目录。prepare 创建该目录；record、
verify、mock、export 和 finish 打开该目录。`session.json` 是工具拥有的内部
descriptor，调用方只传目录，不定位或修改其中的文件。

### prepare

`prepare` 是唯一接受 source 选择的命令。它要求恰好给出一个初始来源：

- `--case CASE`：官方 case。工具复制 case 的不可变来源；存在内置材料时同时导入
  current recording，没有内置材料时 current 为空。
- `--plan FILE`：消费者 plan。工具建立录制环境并复制 plan；初始 current 为空。
- `--recording PATH`：已有录制材料。工具把完整材料包复制进 session，并把副本设为
  current recording。

这三种来源可以理解为两种初始状态：

1. 为录制准备的干净环境，来源是 case 或 plan。
2. 已有录制材料，来源是 recording，可以调用 verify、mock 或 export；verify 是否
   可执行取决于材料是否有适用官方 verifier。

这只是对初始状态的分类，不是权限模型。prepare 完成后，record、verify 和 mock
都可以被调用；它们各自检查真实前提，缺失前提时在创建 execution 前失败。source
类型不会在 CLI 或路由层禁止某个动作。

prepare 完成以下工作：

- 校验 source、agent type、材料格式和哈希。
- 创建 session 目录、工具拥有的临时运行根、环境基线和连接槽。
- 将 case、plan 或已有录制材料完整复制到 session，并校验副本哈希。
- 固化不可变的 record source：case 使用官方 plan 和 driver，plan 使用消费者 plan
  和外部执行器，recording 使用材料内的 plan、binding 和 provenance 推导的执行器。
- 将当前材料写为 current recording；没有材料时保持为空。
- 写入只读 `session.json` 和可变状态索引，不启动客户端、SDK、driver 或真实 Agent。

prepare 不修改外部材料。session 目录必须尚不存在；来源路径不能位于目标 session
内。prepare 失败不产生可用 session，失败诊断保存在 session 内或 prepare 的诊断
文件中。

### 多个 action execution

prepare 成功后 session 处于 open 状态。每次 record、verify 或 mock 都创建新的
execution：

1. 通过前置检查后才领取 execution id。
2. 从不可变环境基线派生本次可写 overlay。
3. 启动真实 Agent、官方 driver 或回放连接，读写 session 中的 current recording。
4. 完成连接、进程、收尾和 receipt 持久化。
5. 原子更新 execution index；record 成功时额外原子切换 current recording。

同一 session 的 action 串行执行；同一 execution 内声明的不同连接可以并发。一个
action 完成或失败后，下一个 action 才能开始。record、verify、mock 可以任意重复，
不要求成对出现，也不要求上一次 verify 成功。

### finish

`finish` 关闭 session，但不代表某个 action 成功。它读取全部 execution，核验
execution descriptor、receipt、材料哈希、连接完整消费、EOF、进程退出和清理结果，
写入 `finish.json`，然后删除工具拥有的临时运行根。

finish 与 action 共用会话锁。若 action 正在执行，finish 在写入任何状态或清理任何
目录前失败；调用方必须等待 action 完成。finish 领取收尾后 session 进入 closed，
禁止创建新的 execution，但保留只读 export 和可重试的清理。清理失败不会重新打开
session；再次 finish 只处理未完成的清理。

finish 不自动执行 record、verify、mock、export 或 admission，也不删除 session
目录、material、execution 或调用方文件。finish 可重试；清理失败保留
`cleanup.json`，后续 finish 只重试未完成的清理。

finish 后不能创建新的 action execution。只读 export 仍可执行，因为 session 中的
材料仍然存在。

## 命令与数据边界

| 命令 | 数据选择 | 主要产物 | 是否改变 current recording |
| --- | --- | --- | --- |
| `prepare` | `--case`、`--plan` 或 `--recording` | session、环境基线、初始 material | 初始化，可设置一次 |
| `record` | 仅 session 中的 record source 和 session 设置 | execution、成功后的新 material | 成功时原子替换 |
| `verify` | 仅 session 中的 current recording | execution、verification 证据 | 否 |
| `mock` | 仅 session 中的 current recording | execution、协议与进程 receipt | 否 |
| `export` | 仅 session 中的 current recording | session 外的完整材料包 | 否 |
| `finish` | 仅 session | 会话级证据、closed 状态、清理 | 否 |

`record`、`verify`、`mock` 不接受 `--case`、`--plan` 或 `--recording`，也不接受
直接指向材料的路径。所有材料选择已经在 prepare 完成。`mock` 不接受 Agent 版本
查询或元命令。

record 的 `--cmd` 和 `--` 后参数只描述本次真实 Agent 启动，不改变 session 的
source 或 current recording。verify、mock 的连接和超时等参数属于本次 execution。
具体 flag 可以按实现需要增减，但上述数据边界不变。

## 数据布局与所有权

session 目录是持久控制面和唯一业务数据根：

```text
<session>/
  session.json
  source/
    source.json
    plan.json               # case 或 plan 的不可变来源副本
    history-seed/           # 可选，显式历史种子的不可变副本
  materials/
    index.json
    <materialId>/
      material.json         # 身份、provenance、来源哈希
      plan.json
      manifest.json
      raw/
        raw.jsonl
        manifest.json
        transport.json
        stderr.raw
      config/               # 录制绑定且允许回放的历史子树
      history-seed/
  current.json
  executions/
    index.json
    <executionId>/
      execution.json
      record/               # record 失败时保留的部分采集与诊断
      receipts/
      verification.json     # verify execution
  prepare.json
  finish.json
  cleanup.json
```

`session.json` 只保存 schema、session 身份、工具身份、agent type、source 身份、
环境基线和连接合同，不在 action 执行期间改写。current recording、execution index
和 finish 状态保存在受锁保护的可变文件中。

环境基线位于系统临时目录，由工具拥有并带所有权标记。每个 execution 从基线派生
独立 workspace、HOME、config、tmp 和历史 overlay。session 目录中的 material 和
execution 是持久证据；临时运行根不是数据根，finish 只清理后者。

record 成功时，工具先把完整材料写入新的不可变 material 目录并校验全部哈希，再
原子更新 `current.json`。任何一步失败都不能把半成品设为 current。旧 material
和旧 verification 保留，但不再是 current，也不会被后续动作自动选择。

## 记录、验证与回放

### record

record 使用 prepare 提供的录制环境启动真实 Agent，保存双向原始帧、stderr、plan、
manifest、历史快照、业务报告、采集和清理结果。case source 由官方 driver 执行；
plan source 由消费者自己的 SDK 驱动。

record 成功要求本次采集、材料闭包和清理达到其自身合同。成功后生成新 material，
原子替换 current recording。失败、未触发或不完整的尝试保留 execution、stderr 和
已有原始帧，但不生成 current material，旧 current recording 保持不变。

record 不执行 verify、资格判断或采用。新 current recording 不继承旧材料的
verification，也不因为 payload 或 case 身份相似而共享证据。

plan source 的消费者流程和 route 使用方式见[消费者录制](consumer-recording.md)。
record source 与 current recording 分离：多次 record 始终使用 prepare 固化的同一
plan 和 executor，成功录制只替换 current recording，不改变 record source。缺少
plan、协议 cwd、执行器路由或必要的 agent 参数时，record 在领取 execution 前报告
前提不足；这不是命令级禁令。

### verify

verify 只评价 current recording，并绑定其精确 material 哈希。它不修改、
不消费、不替换 current material，也不读取旧的 action 参数。

verify 只对具有适用官方 verifier 的材料执行。当前材料必须具有精确的官方 case
身份、agent binding 和 provenance；外部 plan 录制、consumer material 或仅自称
official 的材料没有适用 verifier。没有 current recording 或没有适用 verifier
时，verify 在领取 execution 前失败，不产生空 execution。

一次 verify execution 的通过要求：

- material、plan、manifest、raw 和绑定历史哈希有效。
- 官方 driver 对被验证 case 的业务断言通过。
- 所有声明连接严格匹配并完整消费。
- 客户端 EOF、进程退出和工具自有资源清理成功。

验证证据记录 material 哈希、case/binding 身份、工具、driver、实际安装的官方 SDK
身份及每个连接的 receipt。current recording 被新的 record 替换后，旧
verification 仍属于旧 material，但不能证明新材料；verify 结论不能转移或继承。

verify 默认只产出证据。只有显式要求写回仓库时，才在同一精确验证结论上执行资格
检查、私有归档和原子采用；采用失败保留验证证据、旧索引和旧材料。

### mock

mock 只把 current recording 作为原生协议对端。它不检查 current 是否 verified，
也不要求 mock 之前执行过 verify。没有 current recording、材料格式不兼容或连接
声明不可用时，mock 在领取 execution 前失败，不能产生空成功。

mock 不执行录制中的 shell、文件编辑、hooks 或模型调用，也不重建其业务副作用。
它不模拟 Agent CLI 的版本、帮助或其他元命令。消费者负责启动 SDK 宿主、应用
prepare 返回的环境和 workspace，并自行执行与报告业务断言。

每次 mock 创建新 execution。普通流程只有 `main` 连接；只有 case 确实声明多个独立
原生客户端生命周期时才允许多连接。同一 execution 的连接可以并发，但每个声明连接
最多启动一次，并分别完成严格匹配、完整消费、EOF、退出和清理。

### export

export 把 current recording 的完整材料包复制到 session 外：

- 包含 material identity、plan、manifest、raw、绑定历史和来源哈希。
- 不包含整个 session、其他材料、全部 execution、receipt 或 finish 证据。
- 不产生 verified、admission eligibility 或 admitted 结论。
- 不修改 current recording、execution index 或 session 状态。

目标目录必须位于 session 外且尚不存在。工具先写入同父目录下的临时目录，校验副本
哈希，再原子 rename 为目标目录。export 可以重复执行；不同目标互不覆盖。导出包
可以作为下一次 `prepare --recording` 的输入。

export 与 finish 解耦：可以在 session open 时导出，也可以在 finish 后继续导出
持久材料。

### finish

finish 只处理 session 收尾。它不通过成功协议回执推断业务成功，也不把 verify、
mock、export 或 adoption 作为隐式步骤。任何 execution 失败都保留在会话级证据中，
不能被其他成功 execution 抵消。

## 信任与入仓

工具区分四层结论：

| 层级 | 含义 | 产生方式 |
| --- | --- | --- |
| Action success | 某次 record、verify 或 mock 按自身合同完成 | 对应 execution |
| Verification passed | 精确 material 通过官方 verify | verify execution 的证据 |
| Admission eligibility | 材料具备进入默认 case 库的资格 | 显式资格检查 |
| Admitted | 材料已归档并写入默认 case 库 | 显式采用 |

- record 成功只表示采集和材料闭包完成，不表示官方业务验证通过。
- verify 通过只对绑定的 material 哈希有效，不代表已入仓或允许公开发布。
- mock 成功只表示协议回放、退出和清理完成；消费者业务结果由消费者报告。
- export 成功只表示字节复制和哈希校验完成，不提升任何可信等级。
- finish 成功只表示会话收尾完成，不改变材料资格。

只有同时满足以下条件的 material 才具备默认 case 库的入仓资格：

- 身份是精确的官方 case 和 agent binding。
- provenance 为 official。
- 对该精确 material 的官方 verify 通过。
- 资格检查确认来源、隔离、业务结果、材料闭包和哈希均有效。

资格判断和采用都不能由 record、mock、export 或 finish 隐式触发。采用是显式、
原子、可重试的仓库写入；写入前保留旧索引，写入失败不删除已验证材料。

consumer material 和外部用户自行保存的录制可以进入自己的 session、由用户自己的
验证流程评价并通过 export 保存，但不能冒充官方 case 验证，也不进入默认 case
库或内置快照。

## 不变量

- prepare 是外部材料进入 session 的唯一入口；prepare 完成后 action 不再读取原始
  外部材料路径。
- prepare 完整复制并校验副本；删除 prepare 的原路径不能影响后续 verify、mock 或
  export。
- 一次 prepare 可以承载多个 action execution；source 类型不限制可调用的业务动作。
- session 同一时刻只执行一个 action；同一 execution 的不同连接可以并发。
- 每个 action execution 有唯一 id、不可变 descriptor、独立 overlay 和最终 receipt。
- record 只有成功完成材料闭包后才原子替换 current recording；失败不能部分更新。
- current recording 始终指向一个完整、不可变且哈希有效的 material，或保持为空。
- verify 只读 current recording，不修改材料；verification 绑定精确 material 哈希。
- 新 record 不继承旧 verification，旧 verification 不自动适用于新 current。
- mock 不检查 verified，也不要求 verify 先于 mock。
- verify 缺少 current recording 或适用官方 verifier 时，在领取 execution 前失败。
- mock 缺少 current recording、材料格式不兼容或连接声明不可用时，在领取 execution
  前失败。
- finish 不删除 session 数据，不自动执行 verify、mock、export 或 admission。
- export 不改变 session，不产生可信结论，目标必须在 session 外且不能覆盖已有目录。
- 只有精确官方 case material 的 verify 通过可以作为入仓资格的一部分；mock 成功、
  export 成功和 finish 成功都不能替代。

## 失败与证据

失败分为领取 execution 前后两类：

- **领取前失败**：session 不存在、已关闭或忙碌；record 没有可执行的 record source；
  verify 没有 current recording 或适用官方 verifier；mock 没有 current recording
  或兼容的连接声明。工具不创建 execution，直接返回诊断。
- **领取后失败**：真实 Agent、driver、SDK 或回放连接在执行中失败。工具必须保留
  execution descriptor、已产生的原始帧、stderr、连接 receipt、进程状态和清理结果，
  并按统一 finalize 路径结束 execution。

record 领取后失败时，current recording 不变；verify 失败时 current recording 不变；
mock 失败时 current recording 不变。任何失败都不能删掉旧材料或旧验证证据。

工具按以下顺序持久化：先写原始证据和 receipt，再写 execution 结论，最后更新
execution index 和 current pointer。索引更新失败时，未索引的 execution 或 material
保持可诊断状态，不能伪装成成功。

凭证、认证变量和原生私有配置只写入工具拥有的临时根或私有运行文件，不进入
session.json、material、execution、export 包或 finish 证据。

## 环境与隔离

prepare 为 session 创建独立环境基线，包括 workspace、HOME、config、tmp、历史种子
和连接槽。每次 action 从基线派生可写 overlay，一个 execution 的副作用不能进入
另一个 execution。Codex 使用 `CODEX_HOME`，Claude 使用 `CLAUDE_CONFIG_DIR`。

认证只来自工具允许的原生配置和当前进程中的白名单变量。工具不复制个人 skills、
插件、记忆、项目指令或无关会话；不把完整 Agent home 当作 session 数据。

plan source 的消费者必须在启动自己的 SDK 宿主前应用当前 route 的 env、workspace、
历史和连接命令；只配置 record 或 mock 子进程不能配置父进程。消费者不能覆盖隔离
路径或原生环境变量。

record 完成时只快照回放所需的原生 session/history 子树，并记录来源、派生路径和
哈希。verify 和 mock 在 execution overlay 中产生的宿主状态不会自动提升为新的
material。finish 清理临时根后，session 中的材料仍可由 export 读取。

## Case 与 agent type 配置

[场景目录](../data/scenarios)保存用户故事、case、输入、预期和 agent binding。
case 身份、输入顺序、required/optional、`live-selected-path`、`planned` 和
`unsupported` 都是权威数据；人工构造的验证输入不能替代真实使用流程。

`--agent codex|claude` 在 source 能唯一确定时可省略：plan 从自身声明推断，
recording 从 manifest 推断，case 只有在只有一个可用 binding 时才自动选择；存在
多个 binding 时必须显式给出 `--agent`。显式值与推断值不一致时 prepare 失败。

外部 plan 的 caseId 是消费者自定义身份，不是官方 catalog case。它参与自身 session
的 plan 和 manifest 绑定，但不参与官方 case、资格或内置材料选择。

新增 case 同时更新场景、agent binding、验收规范、测试和固定索引要求；不能只增加
一条成功录制或降低现有 required 集合。全部用户目标和通过边界见
[共同验收](cases.md)。

## 严格回放

录制使用 stdio JSONL v1。每帧保留 `ordinal`、`direction`、`channel`、`epoch`、
`elapsedMs` 和原始 `wire` JSON；`wire` 不从业务对象重建，未知字段、键顺序和数字
字面量保持不变。

回放按保存顺序发送服务端帧，以客户端请求作为对应输出的屏障。工具比较完整客户端
消息，只按协议 Adapter 的明确规则重绑定关联 ID。以下情况必须失败：

- 请求内容、顺序、连接或协议版本不匹配。
- 等待超时、客户端提前 EOF 或存在未消费的服务端帧。
- 来源、plan、manifest、raw 或绑定历史哈希无效。
- 原生进程异常退出或工具自有资源未清理。

mock 不支持的分支或协议明确失败，不搜索其他 material、不猜测 agent type、不启动
真实 Agent。consumer material 使用同一回放引擎，不会因为来源是消费者而增加通配、
字段忽略或 UUID 自动生成规则。

## 测试边界

工具通过 installed CLI 和消费者实际使用的进程接口验证 session 合同，不直接调用
lifecycle 函数。至少覆盖：

- prepare 完整复制材料后，删除原始路径仍可 verify、mock 和 export。
- record 成功原子替换 current；record 失败保留 execution 且 current 不变。
- verify 只作用于 current，并在新 record 后不把旧 verification 转给新材料。
- 没有 verified 的 current material 可以正常 mock。
- 同一 session 的 action 串行；同一 execution 的多连接可以并发。
- 没有 current 或适用 verifier 时，verify 在领取 execution 前失败。
- 没有 current 或连接声明不可用时，mock 在领取 execution 前失败。
- action 执行期间 finish 在修改状态或清理目录前失败；closed 后不能创建新 execution。
- mock 不响应 Agent CLI 版本或元命令。
- export 可重复、不改变 session、不产生入仓结论，导出包可再次 prepare。
- finish 不删除 session 和材料；finish 后可以 export，但不能创建新 execution。
- 只有精确官方 case material 的 verify 通过才具备入仓资格；consumer material 和
  未验证材料不能进入默认 case 库。

默认测试全程离线，不启动真实 Agent、不新增录制。真实录制、官方验证、SDK 仿真和
产品 E2E 分别报告，不能跨证据层抵扣。当前材料状态、缺口和官方环境限制分别由
[data/index.json](../data/index.json)、
[data/builtin/manifest.json](../data/builtin/manifest.json) 和
[driver 环境说明](../drivers/README.md#官方-sdk-环境)提供。
