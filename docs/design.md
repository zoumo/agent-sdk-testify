# agent-sdk-testify 工具设计

`agent-sdk-testify` 是面向多语言 SDK 的独立录制与回放工具。它维护 case、
官方 driver、录制材料、固定索引和内置快照；SDK 业务测试由消费者自己编排。

公开 CLI 分为会话、协议对端和官方编排三组：

```text
agent-sdk-testify
  prepare
  agent
    record
    mock
  official
    record
    verify
  export
  finish
```

prepare、finish 管理会话生命周期，export 只读导出 current。agent record/mock
是 SDK 启动的协议对端；official record/verify 编排官方 SDK driver 并保存业务事实。
本文单独使用 record、mock 时指 agent record、agent mock，验证指 official verify；
涉及官方录制编排时使用完整名称 official record。仓库路径、编码和测试规则见
[开发规范](development.md)。

## 问题与目标

录制、验证、回放和消费者测试通常需要不同的隔离环境，但不应因此产生多套生命周期。
本工具用一次 `prepare` 建立一个自包含 session，之后在同一 session 中反复执行
协议执行或官方编排，直到显式 `finish`。

目标如下：

- `session-dir` 是唯一数据根。外部材料只在 prepare 时进入 session，后续动作不再
  读取原路径。
- 一次 prepare 可以承载多个独立 action execution。record 成功后产生的录制成为
  current recording，后续 verify 和 mock 默认消费它。
- agent record 负责采集，agent mock 负责回放，两者拥有各自的 execution。
  official record/verify 只编排官方 driver 并记录业务事实；driver 调用公开 agent
  命令，协议事实来自 agent 命令自己管理的 execution 和 receipt。
- verified 只表示某个精确材料通过了验证，不是 mock 的前置条件，也不是材料进入仓库
  的充分条件。
- 外部消费者可以自行准备 plan、录制材料和业务验证；官方 case 仓库只接收满足官方
  验证和入仓条件的材料。

## 非目标

- 不编排消费者的 SDK 业务断言、产品 E2E 或仿真门禁。
- 不模拟 Agent CLI 的版本查询、帮助或其他元命令；mock 只提供协议回放。
- 不强制录制、官方验证、消费者回放的顺序；各命令按真实前提检查可执行性。
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
| Action execution | 一次 record 或 mock 产生的 execution，具有独立 descriptor、overlay、receipt 和失败证据。 |
| Receipt | 连接或进程的持久结果，记录协议、退出、清理和诊断。 |
| Verification | 一次官方验证的事实与结论，绑定精确 material、driver 身份及实际参与的 mock execution；不拥有协议连接或 execution。 |
| Admission | 把具备资格的材料显式归档并写入默认 case 库的动作。 |
| Export | 把 current recording 的完整材料包复制到 session 外的只读导出动作。 |
| Finish | 关闭 session、汇总 execution 证据并清理工具拥有的临时运行环境的阶段。 |

Material 是回放输入，不必先经过 verify。Verified 是 material 上的一份证据，不是
material 的固有类型，也不会随着 current recording 切换而转移。

## 会话生命周期

规范流程是：

```sh
agent-sdk-testify prepare --case SDK-CASE-001 --session-dir /private/session
agent-sdk-testify official record --session-dir /private/session \
  --cmd /absolute/agent -- <native-args>
agent-sdk-testify official verify --session-dir /private/session
agent-sdk-testify agent mock --session-dir /private/session
agent-sdk-testify export --session-dir /private/session \
  --output /private/exported-recording
agent-sdk-testify finish --session-dir /private/session
```

`--session-dir` 在所有命令中表示同一个绝对目录。prepare 创建该目录；agent record/mock、
official record/verify、export 和 finish 打开该目录。`session.json` 是工具拥有的内部
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

这只是对初始状态的分类，不是权限模型。prepare 完成后，agent record/mock、official record/verify
都可以被调用；它们各自检查真实前提，缺失前提时在启动执行前失败。source
类型不会在 CLI 或路由层禁止某个动作。

prepare 完成以下工作：

- 校验 source、agent type、材料格式和哈希。
- 创建 session 目录、工具拥有的临时运行根、环境基线和连接槽。
- 将 case、plan 或已有录制材料完整复制到 session，并校验副本哈希。
- 固化不可变的 record source：保存 plan、协议 cwd、agent binding 和来源身份。
  case 的官方 driver 由 official 命令选择，plan 的 SDK 宿主由消费者启动；agent
  record 不根据 source 类型启动 driver。recording 使用材料内的录制来源。
- 将当前材料写为 current recording；没有材料时保持为空。
- 写入只读 `session.json` 和可变状态索引，不启动客户端、SDK、driver 或真实 Agent。

prepare 不修改外部材料。session 目录必须尚不存在；来源路径不能位于目标 session
内。prepare 失败不产生可用 session，失败诊断保存在 session 内或 prepare 的诊断
文件中。

### 多个 action execution

prepare 成功后 session 处于 open 状态。agent record/mock 创建 execution；official record/verify
保存独立的编排事实，不创建协议 execution：

1. 通过前置检查后才领取 execution id。
2. 从不可变环境基线派生本次可写 overlay。
3. agent record 启动或连接真实 Agent，agent mock 提供回放连接；材料来自 session。
4. 完成连接、进程、收尾和 receipt 持久化。
5. 原子更新 execution index；record 成功时额外原子切换 current recording。

同一 session 的 record/mock execution 串行执行；同一 execution 内声明的不同
连接可以并发。official 编排期间，driver 按普通合同启动 agent record 或 agent mock；
不预分配协议 execution，不增加跳过 execution 的官方专用模式。

会话同时只允许一个 official 编排，并登记其运行状态。official verify 固定 current
material，期间拒绝 agent record、official record 和另一次 official verify，允许
agent mock。official record 允许其 driver 调用 agent record；不允许另一次 official
编排交错。官方录制的 execution 归属通过正常结果接口明确关联，不能靠最新目录推测。

official 编排登记与 execution 创建、finish 的前置检查受同一会话状态协调；official
命令等待 driver 时不持有 agent 命令需要的 execution 锁。finish 在任一编排或
execution 活跃时失败，包含 driver 尚未启动协议进程和协议进程已退出但 driver
尚未结束的时段。编排结束前等待 driver 及其启动的 agent 进程退出，保存事实后
解除活动登记。异常中断遗留的活动记录必须确认相关进程已结束后才能解除。

agent 命令与 official 命令可以重复调用，不要求成对出现，也不要求上一次验证成功。

### finish

`finish` 关闭 session，但不代表某个 action 成功。它读取全部 execution、官方编排记录和 verification，核验
execution descriptor、receipt、材料哈希、连接完整消费、EOF、进程退出和清理结果，
写入 `finish.json`，然后删除工具拥有的临时运行根。

finish 按上述会话协调规则检查活动 execution 和 official 编排。若仍有执行或验证
正在进行，finish 在写入任何状态或清理任何目录前失败。finish 领取收尾后 session 进入 closed，
禁止创建新的 execution 或 official 编排，但保留只读 export 和可重试的清理。清理失败不会重新打开
session；再次 finish 只处理未完成的清理。

finish 不自动执行 record、verify、mock、export 或 admission，也不删除 session
目录、material、execution、官方编排记录、verification 或调用方文件。finish 可重试；清理失败保留
`cleanup.json`，后续 finish 只重试未完成的清理。

finish 后不能创建新的 action execution 或 official 编排。只读 export 仍可执行，因为 session 中的
材料仍然存在。

## 命令与数据边界

| 命令 | 数据选择 | 主要产物 | 是否改变 current recording |
| --- | --- | --- | --- |
| `prepare` | `--case`、`--plan` 或 `--recording` | session、环境基线、初始 material | 初始化，可设置一次 |
| `agent record` | 仅 session 中的 record source 和 session 设置 | execution、成功后的新 material | 成功时原子替换 |
| `official record` | session 中的官方 case/binding 和 record source | driver 事实、record execution/material 引用 | 由 agent record 成功发布时替换 |
| `official verify` | 仅 session 中的 current recording | driver 事实、mock execution 引用、verification 证据 | 否 |
| `agent mock` | 仅 session 中的 current recording | execution、协议与进程 receipt | 否 |
| `export` | 仅 session 中的 current recording | session 外的完整材料包 | 否 |
| `finish` | 仅 session | 会话级证据、closed 状态、清理 | 否 |

agent record/mock 和 official record/verify 不接受 `--case`、`--plan` 或 `--recording`，也不接受
直接指向材料的路径。所有材料选择已经在 prepare 完成。`agent mock` 不接受 Agent 版本
查询或元命令。

agent record、official record 的 `--cmd` 和 `--` 后参数只描述本次真实 Agent 启动，
远程连接参数描述已有服务，不改变 session 的
source 或 current recording。mock 的连接和超时参数属于本次 execution；verify 的
超时属于本次验证，不创建连接或 execution。
具体 flag 可以按实现需要增减，但上述数据边界不变。

## 数据布局与所有权

session 目录是持久控制面和唯一业务数据根：

```text
<session>/
  session.json
  source/
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
  official-records/
    <runId>/
      result.json           # driver 事实、record execution/material 引用及编排结果
      driver/               # 业务报告、stdout/stderr、退出和失败诊断
  verifications/
    <verificationId>/
      verification.json     # 输入身份、driver 事实、mock execution 引用及结论
      driver/               # 业务报告、stdout/stderr、退出和失败诊断
  prepare.json
  finish.json
  cleanup.json
```

`session.json` 只保存 schema、session 身份、工具身份、agent type、source 身份、
环境基线和连接合同，不在 action 执行期间改写。current recording、execution index
和 official 编排活动登记、finish 状态保存在受锁保护的可变文件中。

环境基线位于系统临时目录，由工具拥有并带所有权标记。每个 execution 从基线派生
独立 workspace、HOME、config、tmp 和历史 overlay。session 目录中的 material 和
execution、官方编排记录、verification 是持久证据；临时运行根不是数据根，finish 只清理后者。

record 成功时，工具先把完整材料写入新的不可变 material 目录并校验全部哈希，再
原子更新 `current.json`。任何一步失败都不能把半成品设为 current。旧 material
和旧 verification 保留，但不再是 current，也不会被后续动作自动选择。

## 记录、验证与回放

### agent record

agent record 是 SDK 启动的录制代理。它使用 prepare 提供的录制来源和隔离环境，
创建 execution，启动或连接真实 Agent，转发双向协议并保存原始帧、stderr、plan、
manifest、历史快照、采集和清理结果。它不启动官方 driver，也不编排 SDK 业务。
官方 driver 与消费者 SDK 宿主都使用这一公开进程接口。

record 成功要求本次采集、材料闭包和清理达到自身合同。成功后生成不可变 material，
原子替换 current recording。失败或不完整的采集保留 execution、stderr 和已有帧，
不发布半成品。record 不执行官方验证、资格判断或采用，新材料不继承旧验证证据。

record 不等待父 driver 的最终业务报告，也不以业务断言结果决定采集是否成功。
material 在采集完成时封闭；driver 随后生成的业务报告保存在官方编排记录中，通过
execution ID、material ID 和哈希关联，不回写已发布 material。采集通过与官方
业务通过分别记录；后续业务失败不撤销已经完成的采集，也不能把编排结果标成成功。

record source 与 current recording 分离：每次 record 使用 prepare 固化的同一
plan、协议 cwd 和来源身份。缺少 plan、协议 cwd、目标或必要参数时，在领取
execution 前失败。plan source 的消费者流程见[消费者录制](consumer-recording.md)。

### official record

official record 选择当前 session 的精确官方 case/binding 和 driver，准备 SDK
宿主环境并启动 driver；driver 通过官方 SDK 启动公开 agent record，后者才负责
真实 Agent 和录制 execution。official record 只持有编排状态、driver 进程及业务
事实，不创建自己的协议 execution 或第二套 overlay。消费者 plan 无适用官方
录制 driver 时，在启动 driver 前失败；消费者仍可直接使用 agent record。

official record 向 driver 提供 agent record 的命令、真实 Agent 目标和参数；driver
必须使用该命令，不得直接绕过录制代理启动真实 Agent。official record 保存实际
SDK/driver 身份、业务报告、退出状态及实际 record execution/material 的精确引用。
SDK 宿主应用 agent record 的正常环境合同。启动失败、未触发、业务失败和采集失败
分别保留事实，不把 driver 成功退出等同于录制完整。

编排成功要求官方业务断言通过、所需录制完整且进程退出和清理成功。官方录制事实
与 material 来源身份必须可核验；仅给外部采集填写官方 case 名称不能取得官方资格。
编排事实缺失或失败时不能据此采用材料，仍须遵循既有官方验证与资格合同。

### official verify

verify 只评价 current recording，并绑定其精确 material 哈希。它不修改、
不消费、不替换 current material，也不读取旧的 action 参数。

verify 只对具有适用官方 verifier 的材料执行。当前材料必须具有精确的官方 case
身份、agent binding 和 provenance；外部 plan 录制、consumer material 或仅自称
official 的材料没有适用 verifier。没有 current recording 或没有适用 verifier
时，verify 在启动 driver 前失败，不产生空验证成功。

official verify 只负责驱动和记录事实：

1. 校验并固定 current material 的身份和哈希，登记本次 verification。
2. 选择官方 driver，提供正常的公开 mock 命令和 SDK 宿主环境。
3. 启动 driver，由 driver 使用官方 SDK 调用公开 `agent mock --session-dir ...`；需要
   多连接时使用材料声明的连接参数。mock 自己创建和结束 execution、管理 overlay、
   连接、receipt 和清理，verify 不预分配这些对象，也不覆盖 mock 的材料选择。
4. 保存 driver 的业务报告、实际 SDK 身份、退出状态和诊断，核对本次实际调用的
   mock execution、连接 receipt 和进程退出事实，写入 verification。

driver 必须明确报告实际调用对应的 execution ID 和连接 ID；verify 核对这些引用
属于当前 session、绑定本次固定 material，且覆盖所需的全部连接。不能通过扫描
目录、选择最新 execution 或复用其他成功回执来猜测归属。普通 mock 也使用相同的
execution 身份与结果接口，不增加内部回放命令或跳过 execution 的 verify 模式。
SDK 宿主应用 mock 的正常环境合同；verify 不创建第二套协议 overlay。

verify 管理自己启动的 driver 的超时、取消、等待退出和诊断。driver 负责关闭 SDK
及其启动的 mock 并记录退出事实；mock 负责自身资源清理。driver 提前退出、mock
未启动、未退出或缺少证据都必须记录为失败；verify 不能替 mock 生成成功 receipt。

一次 verification 的通过要求：

- material、plan、manifest、raw 和绑定历史哈希有效。
- 官方 driver 对被验证 case 的业务断言通过。
- 所有声明连接严格匹配并完整消费。
- 客户端 EOF、进程退出和工具自有资源清理成功。

验证证据记录 material 哈希、case/binding 身份、工具、driver、实际安装的官方 SDK
身份、driver 进程结果及本次 mock execution/连接的精确引用。协议 receipt 保存在
mock execution 中，verification 引用并核验其内容哈希。current recording 被新的 record 替换后，旧
verification 仍属于旧 material，但不能证明新材料；verify 结论不能转移或继承。

verify 默认只产出证据。只有显式要求写回仓库时，才在同一精确验证结论上执行资格
检查、私有归档和原子采用；采用失败保留验证证据、旧索引和旧材料。

### agent mock

mock 只把 current recording 作为原生协议对端。它不检查 current 是否 verified，
也不要求 mock 之前执行过 verify。没有 current recording、材料格式不兼容或连接
声明不可用时，mock 在领取 execution 前失败，不能产生空成功。

mock 不执行录制中的 shell、文件编辑、hooks 或模型调用，也不重建其业务副作用。
它不模拟 Agent CLI 的版本、帮助或其他元命令。消费者负责启动 SDK 宿主、应用
prepare 返回的环境和 workspace，并自行执行与报告业务断言。

每次 mock 创建新 execution。普通流程只有 `main` 连接；只有 case 确实声明多个独立
原生客户端生命周期时才允许多连接。同一 execution 的连接可以并发，但每个声明连接
最多启动一次，并分别完成严格匹配、完整消费、EOF、退出和清理。

官方 driver 与外部消费者使用同一个公开 agent mock 进程接口和 execution 合同。
official verify 不直接调用协议回放函数替代公开 agent mock。

### 远程录制与命令边界

agent record 内部提供 stdio 与认证 WebSocket 的双向桥接，不提供独立 connect
命令。目标由调用方显式声明为本地 executable/argv 或已有远程 endpoint，两者
互斥；远程认证通过私有运行文件传入，不进入 session 的持久证据。official record
可按官方 case 需要准备远程服务并将目标交给 driver，driver 仍启动 agent record。

本地与远程目标复用相同的采集、execution、原始字节和 receipt 合同。远程模式
不启动本地 Agent CLI；record 只关闭自己建立的 WebSocket 连接，不把断开连接
等同于终止远端服务，不发送隐式服务关闭 RPC。官方编排启动的服务由编排负责等待
和清理；调用方已有服务不属于 record 的清理对象。保留认证校验、帧边界、字节限制、
双向转发、客户端 EOF、超时、取消和连接关闭的失败证据。

不提供 internal-protocol 入口及其 record、mock、connect、version 子命令，也不
保留顶层 record、mock、verify 别名。工具身份统一使用顶层 version。协议实现仅在
内部函数间复用，包布局见[开发规范](development.md#代码布局)。

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
mock、export 或 adoption 作为隐式步骤。任何 execution 或官方编排失败都保留在会话级证据中，
不能被其他成功 execution 抵消。

## 信任与入仓

工具区分四层结论：

| 层级 | 含义 | 产生方式 |
| --- | --- | --- |
| Action success | 某次 agent 协议执行或 official 编排按自身合同完成 | agent execution 或官方编排结果 |
| Verification passed | 精确 material 通过官方 verify | verification 引用的 driver 事实与 mock execution 证据 |
| Admission eligibility | 材料具备进入默认 case 库的资格 | 显式资格检查 |
| Admitted | 材料已归档并写入默认 case 库 | 显式采用 |

- record 成功只表示采集和材料闭包完成，不表示官方业务验证通过。
- official record 成功要求采集与官方业务事实均完整，不表示已通过 official verify 或采用。
- verify 通过只对绑定的 material 哈希有效，不代表已入仓或允许公开发布。
- mock 成功只表示协议回放、退出和清理完成；消费者业务结果由消费者报告。
- export 成功只表示字节复制和哈希校验完成，不提升任何可信等级。
- finish 成功只表示会话收尾完成，不改变材料资格。

只有同时满足以下条件的 material 才具备默认 case 库的入仓资格：

- 身份是精确的官方 case 和 agent binding。
- provenance 为 official。
- 官方录制业务事实完整、通过并与精确 material 关联；已有材料内的原生业务证据
  保留原字节，新增编排记录通过身份和哈希绑定，不以缺少事实推断成功。
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
- session 执行与验证的并发规则见[多个 action execution](#多个-action-execution)；
  verify 必须允许其 driver 调用公开 mock。
- 每个 action execution 有唯一 id、不可变 descriptor、独立 overlay 和最终 receipt。
- record 只有成功完成材料闭包后才原子替换 current recording；失败不能部分更新。
- current recording 始终指向一个完整、不可变且哈希有效的 material，或保持为空。
- verify 只读 current recording，不修改材料；verification 绑定精确 material 哈希。
- 新 record 不继承旧 verification，旧 verification 不自动适用于新 current。
- mock 不检查 verified，也不要求 verify 先于 mock。
- verify 不拥有 execution 或协议连接；缺少 current recording 或适用官方 verifier 时，
  在启动 driver 前失败。
- mock 缺少 current recording、材料格式不兼容或连接声明不可用时，在领取 execution
  前失败。
- finish 不删除 session 数据，不自动执行 verify、mock、export 或 admission。
- export 不改变 session，不产生可信结论，目标必须在 session 外且不能覆盖已有目录。
- 只有精确官方 case material 的 verify 通过可以作为入仓资格的一部分；mock 成功、
  export 成功和 finish 成功都不能替代。

## 失败与证据

record 和 mock 的失败分为领取 execution 前后两类：

- **领取前失败**：session 不存在、已关闭或忙碌；record 没有可执行的 record source；
  mock 没有 current recording
  或兼容的连接声明。工具不创建 execution，直接返回诊断。
- **领取后失败**：真实 Agent、采集传输或回放连接在执行中失败。工具必须保留
  execution descriptor、已产生的原始帧、stderr、连接 receipt、进程状态和清理结果，
  并按统一 finalize 路径结束 execution。

verify 前置检查失败直接返回诊断；开始验证后，无论 driver 启动失败、业务失败、
超时、取消或缺少 mock 证据，都在 verification 中保留已知事实和失败原因。
未产生 mock execution 时明确记录未启动，不补造 execution。已产生的 mock
execution 按 mock 自己的 finalize 合同保留；verification 写入失败不能改变其结论。

official record 同样保留 driver 启动、业务、退出和取消事实，引用已经产生的
record execution/material；没有采集 execution 时明确记录未启动。driver 在采集
发布后失败，编排结果为失败，但已发布材料及其 current 指针不因业务失败回滚。
等待子进程退出后才能结束编排登记；未清理完成不能报告成功。

record 领取后失败时，current recording 不变；verify 失败时 current recording 不变；
mock 失败时 current recording 不变。任何失败都不能删掉旧材料或旧验证证据。

工具按以下顺序持久化：先写原始证据和 receipt，再写 execution 结论，最后更新
execution index 和 current pointer。verification 在 driver 和 mock 的事实落盘后
写入自身结论，之后才可显式采用。索引更新失败时，未索引的 execution 或 material
保持可诊断状态，不能伪装成成功。

凭证、认证变量和原生私有配置只写入工具拥有的临时根或私有运行文件，不进入
session.json、material、execution、官方编排记录、verification、export 包或 finish 证据。

## 环境与隔离

prepare 为 session 创建独立环境基线，包括 workspace、HOME、config、tmp、历史种子
和连接槽。每个 record/mock execution 从基线派生可写 overlay，一个 execution 的副作用不能进入
另一个 execution。Codex 使用 `CODEX_HOME`，Claude 使用 `CLAUDE_CONFIG_DIR`。

认证只来自工具允许的原生配置和当前进程中的白名单变量。工具不复制个人 skills、
插件、记忆、项目指令或无关会话；不把完整 Agent home 当作 session 数据。

plan source 的消费者必须在启动自己的 SDK 宿主前应用当前 route 的 env、workspace、
历史和连接命令；只配置 record 或 mock 子进程不能配置父进程。消费者不能覆盖隔离
路径或原生环境变量。

record 完成时只快照回放所需的原生 session/history 子树，并记录来源、派生路径和
哈希。官方验证和消费者在 mock execution overlay 中产生的宿主状态不会自动提升为新的
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
- verify 不创建 execution；driver 必须调用公开 mock，由 mock 管理 execution。
- verification 精确引用本次实际启动的 mock execution，不能采用其他调用的成功证据。
- 验证期间 record、重复 verify 和 finish 失败，但 driver 的公开 mock 不被会话锁阻塞。
- 同一 session 的 record/mock execution 串行；同一 execution 的多连接可以并发。
- 没有 current 或适用 verifier 时，verify 在启动 driver 前失败。
- 没有 current 或连接声明不可用时，mock 在领取 execution 前失败。
- action 执行期间 finish 在修改状态或清理目录前失败；closed 后不能创建新 execution。
- driver 启动失败、提前退出、验证取消和 mock 缺失时保留验证事实；未完成的验证
  不能产生成功结论或允许清理仍在使用的资源。
- 旧顶层 record/mock/verify 和整个 internal-protocol 入口被拒绝。
- official record → driver → agent record 与 official verify → driver → agent mock
  都通过真实 installed CLI 接口；agent record 绝不再次启动 driver。
- driver 最终业务报告与 material 分开保存；采集成功后业务失败不篡改 material。
- agent record 的远程目标保留认证、双向采集、EOF、取消和连接关闭证据，不终止
  调用方已有服务，也不启动本地 Agent。
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
