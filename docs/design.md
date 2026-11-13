# agent-sdk-testify 工具设计

`agent-sdk-testify` 是面向多语言 SDK 的独立录制与回放工具。它维护 case、
官方 driver、录制材料、固定索引和内置快照；SDK 业务测试由消费者自己编排。

公开 CLI 分为 fixture、协议对端和官方编排三组：

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
```

prepare 创建 fixture，export 只读导出 current。agent record/mock
是 SDK 启动的协议对端；official record/verify 编排官方 SDK driver 并保存业务事实。
本文单独使用 record、mock 时指 agent record、agent mock，验证指 official verify；
涉及官方录制编排时使用完整名称 official record。仓库路径、编码和测试规则见
[开发规范](development.md)。

## 入口与职责

同一 module 提供 CLI 和 Go 库两种入口，二者是同一应用核心的薄适配器：

- CLI 入口 `cmd/agent-sdk-testify` 拥有 argv、stdio、descriptor、stderr 和退出码。
- 根包 `agenttestify` 提供类型化的 `Prepare`、`Open`、`Export` 以及 fixture 上的
  `RecordAgent`、`MockAgent`；`RecordAgent` 和 `MockAgent` 沿用调用方的
  `*os.File` 进程流，作为 CLI/录制薄适配。
- 根包另提供 `StartMockAgent` 进程内入口：调用方提供 `StartMockAgentStreams`，
  在 replay 前同步取得 `MockRun.Start`，通过 `FullConsumption`/`Done` 协调 SDK
  半关闭，并用 `Wait` 取得完整的 `ExecutionReport`。它不启动工具二进制。

两种入口共用参数校验、fixture 生命周期、execution 事务和材料规则，不维护第二套机制。
进程内 mock 与 CLI mock 也只共用同一 replay 和 finalize 合同，不另建宽松模式。
`official record`、`official verify`、官方 driver 编排及协议和材料内部类型仅为 CLI
或仓库内部能力，不属于库 v1 接口。

## 问题与目标

录制、验证、回放和消费者测试通常需要不同的隔离环境，但不应因此产生多套生命周期。
本工具用一次 `prepare` 建立一个自包含 fixture，之后在同一 fixture 中反复执行
协议执行或官方编排。调用方管理 fixture 目录的生命周期。

目标如下：

- `fixture-dir` 是唯一数据根。外部材料只在 prepare 时进入 fixture，后续动作不再
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
- 不让 export 或 mock 隐式执行 verify、资格判断或仓库写入。
- 不降低严格回放要求，不为 fixture 复用增加通配、忽略字段或自动生成关联 ID 的规则。

## 领域模型

| 概念 | 定义 |
| --- | --- |
| Fixture | 由 prepare 创建、由调用方管理和删除的运行环境与持久数据根；不是 Agent 原生 session。 |
| Initial source | prepare 选择的 case、plan 或已有 recording；只决定初始状态，不限制后续动作。 |
| Recording package | fixture 中由 material ID 命名的持久包，包含一次成功采集的材料核心，以及引用该材料的 execution、record 和 verification 证据。 |
| Material | recording package 内封闭、不可变的回放输入，包含唯一 material plan、source 原件、manifest、raw、规范历史和来源闭包。 |
| Current recording | fixture 当前选中的唯一 recording package；verify、mock 和 export 以其中的 material 为输入，record 成功时替换它。 |
| Action execution | 一次 record 或 mock 产生的 execution，具有独立 descriptor、overlay、receipt 和失败证据。 |
| Action result | record 或 mock 返回的连接/execution 状态、材料及证据路径。 |
| Receipt | 连接或进程的持久结果，记录协议、退出、清理和诊断。 |
| Verification | 一次官方验证的事实与结论，绑定精确 material、driver 身份及实际参与的 mock execution；不拥有协议连接或 execution。 |
| Admission | 把具备资格的材料显式归档并写入默认 case 库的动作。 |
| Export | 把 current recording 中封闭 material 的原字节复制到 fixture 外的只读导出动作。 |

Recording package 是 fixture 与私有归档共用的持久结构，最终 case 只发布 material。material 封闭后，
后续 execution、record 和 verification 只在该 package 内追加自己的证据域；
每个文件只有一个 owner，material 不记录派生证据或运行目录。

Material 是回放输入，不必先经过 verify。Verified 是精确 material 上的独立证据，
不是 material 的固有类型，也不会随着 current recording 切换而转移。

## Fixture 生命周期

规范流程是：

```sh
agent-sdk-testify prepare --case SDK-CASE-001 --fixture-dir /private/fixture
agent-sdk-testify official record --fixture-dir /private/fixture \
  --cmd /absolute/agent -- <native-args>
agent-sdk-testify official verify --fixture-dir /private/fixture
agent-sdk-testify agent mock --fixture-dir /private/fixture
agent-sdk-testify export --fixture-dir /private/fixture \
  --output /private/exported-recording
```

`--fixture-dir` 在所有命令中表示同一个绝对目录。prepare 创建该目录；agent record/mock、
official record/verify 和 export 打开该目录。`fixture.json` 是工具拥有的内部
descriptor，调用方只传目录，不定位或修改其中的文件。

### prepare

`prepare` 是唯一接受 source 选择的命令。它要求恰好给出一个初始来源：

- `--case CASE`：官方 case。工具复制 case 的不可变来源；存在内置材料时同时导入
  current recording，没有内置材料时 current 为空。
- `--plan FILE`：消费者 plan。工具建立录制环境并复制 plan；初始 current 为空。
- `--recording PATH`：已有录制材料。工具把完整 material 复制进新的 recording
  package，并把该 package 设为 current recording。

这三种来源可以理解为两种初始状态：

1. 为录制准备的干净环境，来源是 case 或 plan。
2. 已有录制材料，来源是 recording，可以调用 verify、mock 或 export；verify 是否
   可执行取决于材料是否有适用官方 verifier。

这只是对初始状态的分类，不是权限模型。prepare 完成后，agent record/mock、official record/verify
都可以被调用；它们各自检查真实前提，缺失前提时在启动执行前失败。source
类型不会在 CLI 或路由层禁止某个动作。

prepare 完成以下工作：

- 校验 source、agent type、材料格式和哈希。
- 创建 fixture 目录及其 runtime 子目录、环境基线和连接槽。
- 将 case、plan 或已有录制材料完整复制到 fixture，并校验副本哈希。
- 固化不可变的 record source：保存 plan、协议 cwd、agent binding 和来源身份。
  case 的官方 driver 由 official 命令选择，plan 的 SDK 宿主由消费者启动；agent
  record 不根据 source 类型启动 driver。recording 使用材料内的录制来源。
- 将导入材料安置到同一 recording package 布局，并选择为 current；没有材料时
  current 为空。
- 写入 schema 7 的只读 `fixture.json` 和可变状态索引，不启动客户端、SDK、driver
  或真实 Agent。工具身份使用 `executable`/`embedded` 判别式：executable 保存版本
  和二进制哈希，本次启动命令在运行时解析并核验身份；embedded 记录模块路径、
  版本/修订和 Go 版本，不伪造 command、launcher 或进程身份。

prepare 不修改外部材料。fixture 目录必须尚不存在；来源路径不能位于目标 fixture
内。prepare 失败不产生可用 fixture，失败诊断保存在 fixture 内或 prepare 的诊断
文件中。schema 6 及更旧 fixture 必须拒绝并重新 prepare，不能按 schema 7 猜测字段。

prepare 的 fixture source 只保存录制起点：source plan 和一份完整冻结的 history
seed。它不复制最终 package 的 material、record 或 verification 布局。每个
execution 从该 seed 按明确绑定 materialize 所需历史；不维护整包 seed 的模板副本，
也不使用可写 hardlink 共享 baseline 与 overlay。模板、route 或宿主目录可以保留
启动前需要的完整可写环境，但它们是本次运行位置，不是历史 delta 格式。

### 导入边界

`--recording` 与有内置材料的 `--case` 使用同一识别器，依次识别以下三类输入；
判别依据是已支持的布局与格式，不是目录名、mtime 或递归搜索到的第一个 plan。

| 输入 | 判别与落点 | 保存与执行边界 |
| --- | --- | --- |
| 完整新 case/package | 根有 `material/material.json`（format 3）；若出现 review/adoption，必须成对且校验其选择闭包 | 原路径、原字节导入 `packages/<materialId>/`，包含所选 record/verify 和 execution 域；没有这些域的合法 package 也不得推断 verified |
| standalone material core | 根有受支持的 `material.json`，或满足明确 legacy material 判别；format 3 的根必须有 plan、manifest 和完整 core 库存 | 原树导入 `packages/<materialId>/material/`；不合成 record、verify 或旧 execution |
| legacy adopted/capture 包 | 按既有无 formatVersion 或 format 2、review/adoption 1 的明确合同识别，可能在根内含旧 official-replay 等证据 | 完整旧树作为只读 legacy material 保存于 `packages/<materialId>/material/`，不把其内证据拆到新域、不改写文件或补 material.json |

多种判别冲突、未声明的新版本、缺失文件、库存之外文件、错误身份或哈希必须拒绝。
legacy 合同明确容许的无版本对象不等于任意缺版本均可接受。已有合法 material ID
必须保留；无 ID 的旧包由 prepare 分配一次 material ID，在 fixture source/control
记录该 ID 与完整导入库存的哈希，不向原树补身份。导入完整新包不复制其域库存到
控制面；无完整库存的 legacy 才在 fixture source 保存一份 importedFiles，并以
按规范相对路径排序的 UTF-8 `path + NUL + sha256 + LF` 序列计算库存 SHA-256。
新 core 的封存身份由 material.json 的原字节 SHA-256 绑定；legacy 缺该文件时，
current 与新 execution 引用上述导入库存 SHA-256 及明确的 legacy 格式判别，不能
把它冒充 material.json 的文件 hash。两个不同内容不能共享同一 ID；
同一 ID 在一次 prepare 内出现冲突直接失败，不覆盖或重新命名伪装成同一材料。

完整包里的旧 fixtureId、executionId、runId、verificationId、原通知和 domain inventory
保持只读。新 fixture 有自己的 fixtureId，调度索引初始为空，只登记自己分配的
execution；导入 execution 不可 join、恢复、复用为本次通过证据。新增 execution/verify
使用新身份，引用导入 core 的原身份与哈希，不改变导入库存或旧 review/adoption 的选择
闭包。完整包原有的 review/adoption 可以留在包根；它们证明原来明确选择的集合，不能
证明后来追加的域。prepare 不复制一份整包到 source，也不把旧 fixture 控制面导入。

record source 由导入 core 中可复原的 source plan、协议身份与完整 seed 闭包冻结到
`source/`；这是后续 record 的起点，不是 current 的动态镜像。缺失可录制前提的合法旧
material 仍能 mock/export；record 在领取 execution 前说明缺失前提。prepare 必须复制
所有后续需要的来源文件；删除原 case/material/plan/seed 路径不影响已准备 fixture。

### 多个 action execution

prepare 成功后 fixture 可反复使用。agent record/mock 创建 execution；official record/verify
保存独立的编排事实，不创建协议 execution：

1. 通过前置检查后才领取 execution id；agent record 同时领取新的 material id，并创建 `packages/<materialId>/`。
2. 从不可变环境基线派生本次可写 overlay。
3. agent record 启动或连接真实 Agent，agent mock 提供回放连接；材料来自 fixture。
4. 完成连接、进程、收尾和 receipt 持久化。
5. record 在材料核心封闭并校验后才提交 current；提交边界与崩溃恢复见
   [封存、发布与恢复](#封存发布与恢复)。

package 目录只由 material id 标识，不新增 package id 或 recording id 别名；
execution id 独立。
失败 record 可以留下尚未封存 material 的 package、该 execution 和部分采集证据，
但 package 不能成为 current，也不能被 verify 或 export 当作材料。多次成功 record
产生多个独立 package；current 只选择一个成功 package，不按时间或最新目录推断。
旧 package 及其历史 verification 保留，但不随 current 切换获得新结论。

同一 fixture 的 record/mock execution 串行执行；同一 execution 内声明的不同
连接可以并发。official 编排期间，driver 按普通合同启动 agent record 或 agent mock；
不预分配协议 execution，不增加跳过 execution 的官方专用模式。

库 `StartMockAgent` 在领取新 execution 或按显式 execution id 加入、claim 本次
connection 并写入 running result 后才返回 `MockRun.Start`；协议 I/O 不早于该快照。
库的新 execution 后续连接通过显式 ExecutionID 加入。CLI 保留现有声明连接领取合同：
锁下只读取当前 fixture index 中唯一 pending execution，核对 action、source/official
origin、target/native、limits 和未 claim 的连接；不扫描目录、不搜索 latest，也不
为这次目录调整增加 CLI execution 预分配步骤。`MockRun.Wait` 只在 replay 与 finalize goroutine 结束后返回；多连接
execution 的最终事实由封存的 execution/result.json 与 `Fixture.ExecutionReport` 给出；
execution index 只负责当前 fixture 的调度与恢复。

fixture 同时只允许一个 official 编排，并登记其运行状态。official verify 固定 current
material，期间拒绝 agent record、official record 和另一次 official verify，允许
agent mock。official record 允许其 driver 调用 agent record；不允许另一次 official
编排交错。官方录制的 execution 归属通过正常结果接口明确关联，不能靠最新目录推测。

official record 在启动 driver 前分配 runId 并登记 `official.json`；official verify
分配 verificationId 并固定 current 的 material ID 与 core 哈希。agent record 通过
前置检查后自己分配 executionId 与 materialId，mock 只分配 executionId 并绑定 current。
ID 都是独立、不复用的本地生成身份；目录组件仅接受非空安全单段 ID，不引入同值
recordingId/packageId 别名。record 的 materialId 是预留包身份，running 时不表示已有
成功 core。收到 running 通知后，官方编排把这个精确身份记入 journal；不能等待最终
成功通知后才记身份，否则会丢失采集失败的归属。

official record 先启动 driver，stdout/stderr 等文件此时唯一 owner 是 runId 对应的
编排域，唯一物理位置暂为 `runtime/official/<runId>/staging/`。agent record 领取
execution 后，通过普通 launcher 的 `--result-fd` 通知给出 material/execution/connection
身份。driver 可以晚于采集完成才产生 report；material core 不等待或吸收这些报告。
所有 writer 关闭、子进程退出或被确认终止后，编排域才结算，细节见封存合同。
编排输出的归属按以下单一规则确定：拿到经校验的 running 身份，即使 core 失败或
未封存，run 也归该 package 的 `record/<runId>/`；只有确认本次从未领取 execution，
才进入 `failures/official-record/<runId>/`。已领取 execution 或是否领取不明时，使用
本次结果接口和已有明确登记的关联复核；无法确认精确归属则保留未结算事实与 staging，
禁止猜测或清理。未收到 running 通知不证明没有 execution/package，不能据此结算
到 no-material 域，也不能扫描 latest。关联复核使用既有结果与活动登记。

无 execution/no-material 失败域与 package record 域互斥。它们保留启动失败、
untriggered、取消、业务失败和实际存在的输出；空 stdout/stderr 仍保留为零输出证据。
`request.json`、`runtime.json`、`materials/case.json` 和 launcher 配置只是运行输入，
不进入持久域。实际业务、失败诊断、case 专用事件或原生服务清理证据仍在本 run 域以其原相对路径
保留并进入该域 inventory，不能因不在通用目录示例里就删除。私有认证/config、可重建
输入和临时 tmp 不作为这些证据。result 保存稳定 invocation 事实：case/binding、源与 canonical plan 哈希、
tool/driver/SDK 身份、启动/退出/清理状态、已知 IDs 和 limits；不引用已删除输入路径。
父 driver 在材料提交后失败，record run 失败，不能入仓，但不回写 core 或回滚 current。

official 编排登记与 execution 创建按同一 fixture 状态协调；official 命令等待 driver 时
不持有 agent 命令需要的 execution 锁。编排结束前等待 driver 及其启动的 agent
进程退出，保存事实后解除活动登记。中断遗留的执行和活动登记保留为未完成事实，
不能被后续成功结果覆盖；调用方负责等待自己启动的全部进程退出。

agent 命令与 official 命令可以重复调用，不要求成对出现，也不要求上一次验证成功。
每次 official record 的新采集形成自己的 package 与 record run；失败/未触发也有
独立 runId。已触发的 run 必须绑定一个实际 record execution（全部连接共享它），不得把
另一次采集的结果挂到旧 package。package 可以保存其导入的历史 record run；后续
本地 official record 不会向旧 core 追加伪造的采集事实。verify 可以对同一 package
重复执行，每次有新 verificationId 和新的 mock execution；结论和库存独立。一次验证只引用本次 driver
实际启动的 mock execution 和连接，不选择 package 内最新执行。

### fixture 目录所有权

调用方拥有整个 fixture 目录。prepare 把运行环境放在 `<fixture>/runtime`，
运行时的 `resourceRoot` 指向该目录；工具不创建 fixture 外的长期运行根。

各 action 在返回前完成自身的协议、材料和资源释放检查，保存成功或失败结果。
mock 在结束时校验本次 execution 绑定的源历史和派生历史；运行中新建的非绑定文件
不成为回放输入。工具不在 fixture 结束时重新审计全部历史 execution。

调用方先关闭 SDK 宿主、等待相关进程全部退出并完成结算，再按需删除 runtime 或整个 fixture。
需要在删除整个 fixture 后保留 current material 时，先 export 到 fixture 外。
整个 fixture 的删除同时移除运行环境、材料和执行证据。fixture 没有 open/closed
状态、关闭命令或 fixture 级最终报告；各次执行和官方编排的结果分别保留。

## 命令与数据边界

| 命令 | 数据选择 | 主要产物 | 是否改变 current recording |
| --- | --- | --- | --- |
| `prepare` | `--case`、`--plan` 或 `--recording` | fixture、环境基线、初始 material | 初始化，可设置一次 |
| `agent record` | 仅 fixture 中的 record source 和 fixture 设置 | execution、成功后的新 material | 成功时原子替换 |
| `official record` | fixture 中的官方 case/binding 和 record source | driver 事实、record execution/material 引用 | 由 agent record 成功发布时替换 |
| `official verify` | 仅 fixture 中的 current recording | driver 事实、mock execution 引用、verification 证据 | 否 |
| `agent mock` | 仅 fixture 中的 current recording | execution、协议与进程 receipt | 否 |
| `export` | 仅 fixture 中的 current recording | fixture 外的完整 material | 否 |

agent record/mock 和 official record/verify 不接受 `--case`、`--plan` 或 `--recording`，也不接受
直接指向材料的路径。所有材料选择已经在 prepare 完成。`agent mock` 不接受 Agent 版本
查询或元命令。

agent record、official record 的 `--cmd` 和 `--` 后参数只描述本次真实 Agent 启动，
远程连接参数描述已有服务，不改变 fixture 的
source 或 current recording。mock 的连接和超时参数属于本次 execution；verify 的
超时属于本次验证，不创建连接或 execution。
具体 flag 可以按实现需要增减，但上述数据边界不变。

## 数据布局与所有权

fixture 目录是持久控制面和唯一业务数据根：

```text
<fixture>/
  fixture.json
  current.json                     # 选择 material 与封存哈希；发布提交点
  official.json                    # 活动登记与结算 journal；空闲时不存在
  executions/index.json            # control/scheduling；不进入 final case
  source/
    plan.json                      # case 或 plan 的不可变来源副本
    history-seed/                  # 唯一完整冻结 seed；fixture source 生命周期
  packages/<materialId>/
    material/
      material.json                # package/material 身份与内容哈希
      source-plan.json             # 用户/case 原始 plan 字节，official 与 consumer 均保留
      plan.json                    # 唯一 material plan
      manifest.json                # 唯一完整 core 文件库存（不含自身与 material.json）
      report.json                  # capture report
      raw/
        raw.jsonl
        manifest.json
        transport.json
        stderr.raw
      history/
        seed/                      # material 内冻结 seed 的规范副本
        runtime/                   # 录制完成时的原生历史快照
      official-origin.json         # 唯一 canonical official origin
      origin/<connectionId>.json   # 逐连接 PID/supervisor 等启动证明
    executions/<executionId>/
      execution.json               # 领取时冻结的声明；状态/后出哈希在 result
      result.json                  # execution 结论、完整域 inventory、core 引用
      materialization.json          # 历史来源→派生目标及双向哈希
      history/derived/              # 仅必要变换后的封存字节，不复制完整历史树
      isolation.json
      prepared-plan.json           # 实际准备事实投影，不复制 material plan
      receipts/
        <connectionId>.json
        <connectionId>.process.json
        <connectionId>.result.json
    record/<runId>/
      result.json                  # 编排结论与精确 execution 引用
      report.json                  # record driver 业务报告
      driver-result.json           # record driver 业务结果
      peers/<connectionId>.json     # supervisor 进程事实与 running 通知
      stdout.log
      stderr.log
    verify/<verificationId>/
      verification.json            # 结论、身份、引用和完整域 inventory
      result.json
      replay-report.json
      driver-result.json
      peers/<connectionId>.json
      stdout.log
      stderr.log
  failures/official-record/<runId>/
    result.json                    # 归属条件见“多个 action execution”的编排输出规则
    stdout.log
    stderr.log
    driver-result.json             # driver 实际产生时才存在
    report.json                    # driver 实际产生时才存在
  prepare.json
  runtime/                         # overlay、私有配置；未结算 staging 不可删除
    routes/<action>.json           # 可重建的 SDK 宿主启动快照；不是持久 descriptor
```

`fixture.json` 只保存 schema、fixture 身份、工具身份、agent type、source 身份、
环境基线和连接合同，不在 action 执行期间改写。current recording、execution index
和 official 编排活动登记保存在受锁保护的可变文件中。

环境基线和每个 execution 的独立 workspace、HOME、config、tmp、历史 overlay
均位于 runtime。`packages/` 保存持久材料与证据；`source/` 只保存录制起点。
`executions/index.json` 是 fixture 控制面的调度索引，不是最终 case 文件。
目录释放时序见[fixture 目录所有权](#fixture-目录所有权)。

`fixture.json`、`current.json`、execution index 和 `execution.json` 共同属于
schema 7 合同。归档 execution 只读，不作为当前 fixture 控制面。`material/` 是 material 核心，
record 在提交前组装并封存，半成品只在明确登记的 staging 中；
封存后不得追加、改写或重写其中任何文件，包括 plan、manifest、history 和历史哈希。
`executions/`、`record/` 和 `verify/` 是 package 内的独立证据域：

- `executions/` 由产生 execution 的 agent record 或 agent mock 写入；普通 mock 与
  official mock 使用完全相同的 descriptor、receipt、finalize 和校验合同。
  `materialization.json` 单独记录历史来源、派生目标及双向哈希；`isolation.json`
  记录有效环境、argv 和 cwd 关系，两类证据不能互相替代。
- `record/<runId>/` 由 official record 在 driver 结算后写入，引用精确 record
  execution；没有 parent 的直接 agent record 不伪造这一域。
- `failures/official-record/<runId>/` 由 official record 保存独立失败事实；归属条件
  只按[多个 action execution](#多个-action-execution)中的编排输出规则判断。
- `verify/<verificationId>/` 由 official verify 写入，引用本次实际 mock
  execution 和连接；普通 mock 不产生 verify 域。

每个对象在每个 package 内只有一个 owner，目录名和文件位置由 ID 推导，不保存第二份
receipt 或指向 runtime staging 的持久引用。一次 record 或 verify 失败时，该域保留
失败结论和已有证据；其他域与 material 不变。同一 package 的多个成功或失败
verification 以独立 ID 并存；record run 与实际采集的关系遵循 execution 分配合同。

material 发布和 current 切换遵循[封存、发布与恢复](#封存发布与恢复)。
旧 package 和旧 verification 保留，但不会被后续动作自动选择。

最终入仓 case 只保存独立 material，attempt 根就是 material 根：

```text
data/<agent>/<story>/<case>/<attempt>/
  material.json
  manifest.json
  plan.json
  raw/...
  history/...
  ...
```

采用前校验完整 package；私有归档保存 review 选择的 material、execution、record、
verification 精确闭包及 `review.json`、`adoption.json`，维持 package 相对路径和原字节。
仓库只复制其中 material 的原字节，不发布旧 execution、record、verify 或证明文件。
固定索引绑定 `materialSHA256`，并记录外部 `adoptionSHA256`、官方 SDK 身份和资格摘要；
材料检查和内置生成只校验索引绑定的本地 core，不依赖归档路径。完整证据只在采用前
作资格判断；摘要不使缺少原录制业务证据的 standalone core 获得再次采用资格。

### 文件 owner 与校验闭包

以下是新格式的唯一所有权表；库存引用同一文件不形成第二个 owner。未列出的 provider
专用原生文件按所属 raw/history 库存保存；不能以减少目录为由丢弃来源或变换证据。

| 文件/域 | 唯一写入者与创建/封存点 | 读取方与保留理由 |
| --- | --- | --- |
| fixture.json、source/plan.json、source/history-seed/ | prepare；发布 fixture 前冻结 | record 的稳定来源；source 完整闭包库存由 fixture source 绑定，不再把整包藏进 baseline/template |
| prepare.json | prepare；成功结果或失败诊断落盘后保留 | prepare 结果与失败原因；不重复 source/core 完整库存 |
| runtime/routes/<action>.json | lifecycle；prepare/route 刷新时原子替换，随 runtime 删除 | CLI 脚本读取的当前宿主启动快照；非证据、不参与材料库存，不复制私有认证值 |
| current.json、executions/index.json、official.json | lifecycle；受同一 fixture 锁协调，可变 | 选择 current、调度当前 fixture 的 execution、恢复明确事务；不作为最终 case 内容 |
| material/plan.json、source-plan.json、manifest.json、report.json | agent record；成功 core 发布时封存 | replay 与来源检查；plan 是唯一派生材料合同，source-plan 是原字节来源，report 只陈述 captured |
| material/raw/ | capture 写入一次；成功时移交 core；失败时留 execution 的 capture/ | 严格回放与采集诊断；raw manifest/transport/stderr 原字节保留，不在 record/verify 复制 |
| material/history/seed/、history/runtime/ | agent record 组装并封存 | source 复验与 native history discovery；具体唯一副本规则见材料计划与运行配置 |
| material/official-origin.json、origin/<connectionId>.json | 已授权的普通 agent record；core 发布前封存 | official provenance 与启动链，后续业务通过不能反向制造它们；consumer 不生成 |
| executions/<id>/execution.json | lifecycle 在领取时创建并冻结 | 声明 action、fixture/source、material ID、连接、limits、native 与工具身份；后出的 core 哈希和状态只进入 result，不回写声明 |
| executions/<id>/prepared-plan.json、isolation.json | 正常 native preparation；执行收尾前完成、随 execution 封存 | 实际准备事实与隔离策略，不用 verify overlay 证明原录制；prepared-plan 只保存实际协议项目、native argv/hash、limits、模型、case/binding、来源闭包哈希和隔离策略等非秘密准备事实；封存时与 canonical/source plan 哈希绑定，不复制整份 plan，不保存待重开的临时输入引用 |
| executions/<id>/materialization.json、history/derived/ | 正常 history materialization；随 execution 封存 | 源/派生的包内引用、原生目标身份、变换类型、双向哈希及必要派生字节；不能并入 isolation 丢失变换信息 |
| executions/<id>/receipts/* | 对应 agent connection；本连接 finalize 后冻结 | 每连接协议/进程/通知结果；进程内连接不生成 process receipt |
| executions/<id>/result.json | lifecycle；全部连接终态并关闭写入后封存 | 唯一 execution 汇总结论、域 inventory、完整连接集合和 core/descriptor 哈希关联 |
| record/<runId>/{report,driver-result,peers/*,stdout,stderr} 及实际 case 证据文件 | 本次 official driver-run；关闭所有 writer 后结算并封存 | 原业务、SDK/launcher 进程事实和零输出证明；汇总不再内嵌复制这些对象 |
| record/<runId>/result.json | official record；结算最后封存 | 独立编排结论、域 inventory、实际 record execution/core 引用、稳定 invocation 事实 |
| verify/<verificationId>/{replay-report,driver-result,peers/*,stdout,stderr} 及实际 case 证据文件 | 本次 official driver-run；关闭 writer 后结算并封存 | 本次验证业务和 launcher 事实；内容即使与 record 相同仍是另一执行的事实，不能共享文件 |
| verify/<verificationId>/result.json、verification.json | official verify；依次封存 driver 进程事实和验证结论 | result 仅持有进程执行事实；verification 是唯一验证结论与域 inventory，引用本次 execution，不 snapshot material/record/fixture |
| failures/official-record/<runId>/ | official record；归属按[编排输出规则](#多个-action-execution)，封存按同一结算合同 | 独立失败事实；不以未知关联伪造无 material/execution |
| review.json、adoption.json | qualification/adoption；显式采用时 | review 选择已封存对象，adoption 绑定 review 哈希与采用身份；不重新保存闭包库存 |

`material.json` 保存 material ID、format、provenance、连接集合及 plan/manifest/raw 哈希，
不再内嵌 `officialOrigin`、完整 `files` 或任意运行位置。official-origin 是唯一完整
origin 对象，逐连接 proof 只引用其哈希与 run/execution/connection 身份，不复制 origin。
跨对象重复 ID/hash 只用于校验边的精确关联，不是对象副本。新 package 不另设 package.json。

prepare 的 source 完整文件库存只在 fixture source 控制合同保存一次；新 core 的库存
与它属于不同生命周期，必须包含可独立复验的完整来源字节。每个封存域只有一份完整文件库存：core 用 `manifest.json.files`，execution 用根
`result.json.files`，record 用其 `result.json.files`，verify 用 `verification.json.files`。
库存覆盖本域所有普通文件（含空文件），排除自己；core 还排除绑定 manifest 的
`material.json`，以免自哈希。manifest 通过 material.json 的 SHA-256 被外层绑定；
其余域结论的 SHA-256 由引用方绑定（例如 review）；没有引用方的独立失败结果仍
保留自己的完整库存与身份，不伪造上游成功引用。core 库存是 material 根相对，其余库存是 package
根相对并限制为本域前缀。库存必须与实际文件集合精确相等，不能只验证所列子集。
原生 raw manifest 的原格式库存继续存在，因为它是采集原件；框架不再生成并列
savedFiles/sourceFiles/driverFiles 的同义全量表或内嵌 report/business/完整 material。

跨域只保存根结论文件的路径、版本、ID、SHA-256 与所选 connection 集合。读取方沿
这些边验证每个域自己的库存，不把被引用域所有 files 再展开一遍。record/verify 引用
execution 的根 result 和最终 descriptor 哈希；verification 引用 core 的 material.json
及 manifest 哈希；review 选择 material ID、record run、verification 和必要 execution
的根结论哈希。adoption 只绑定 review 与目的 case/attempt 身份，不维护第二份选择清单。
不存在覆盖所有 package 追加域的可变“总库存”；旧 review 因而不会因新 verify 失效。

采集中的 raw 只有一个位置：execution 的 `capture/`。成功时停止写入，原子移动到
待封存 core，execution 结果改为引用 core；主连接保留 raw/，probe 保留其原生
remote-probe/raw/ 结构与原字节。移动前 execution index 登记 source、staging、目标
与 material/execution ID；core 发布失败则证据留在唯一位置，由该事务记录标明归属，
直接 agent record 也不依赖 official.json 恢复。失败采集保留 capture/ 与现有 stderr、帧和诊断，不生成假 material。
多连接属于同一 execution，core 按原生连接布局存各自 raw，一次采集只封存一份
execution，不为 main/probe 复制整棵树。

`peers/<connectionId>.json` 只保存监督器自己观察到的 started/reaped/PID/exit/cleanup
事实及 running 通知；最终通知的权威文件是 execution receipt 的 result，peers 只存
其路径与 hash。runtime 的通知传输缓存结算后删除，不再保留内嵌完整 final result 或
第二份 JSONL transcript。running 与最终 result 绑定同一不可变 descriptor 哈希；
后出的 material 哈希由最终 result 与 execution 汇总绑定，不能回写 descriptor。通知字段版本和
根基准与相应落盘对象相同。

### 封存、发布与恢复

material、execution、record、verify 分别封存。封存意味着关闭该域全部 writer、等待
本域拥有的进程与子进程退出、完成 EOF/cleanup 检查、校验身份与完整库存，然后发布
最后的结论文件；封存后不再补字段、重算库存或覆盖结论。失败域也按实际存在的文件
封存，缺报告与零字节日志分别表达，不能把文件不存在当作零输出。

record core 不依赖父 driver：最后一个声明连接完成后，先完成采集与自身资源清理，
停止 raw/history writer，组装 core，校验闭包，原子发布 material/，再以原子替换
`current.json` 作为录制发布提交点。current 包含 material ID 和 core 封存哈希，不能
指向半成品。fixture execution index 在提交前保存明确的 publishing 事务（新旧 current、
execution/material IDs 和已校验 hash），所以恢复不依赖扫描目录或 mtime。连接 passed
不等于整个 execution passed，更不等于已提交 current。

提交点之前失败，旧 current 不变；已封存但未选中的 core 可以留作失败诊断，不冒充
成功 record。提交之后通知写失败、索引收尾失败或父 driver 失败，不能回滚已经可被
读取的 current：返回明确的 delivery/cleanup 失败及 `publicationCommitted: true`、
精确 material ID。提交前该值为 false。通知先原子落盘同一对象再发送；发出的每条完整 JSONL 与其落盘
快照一致。若最终通知只写了部分字节就失败，不能把半行当通知；在 execution 域封存前
持久化 delivery 失败（保留提交事实），通过退出码/stderr 报告，调用方读回权威结果。
成功发出的最终通知不再被事后重写为另一结论。agent result schema 3 同时区分 connection/
execution 状态、采集/发布事实和交付错误，不能用一条 failed 擦掉已提交事实，也不能
在未通知成功时对调用方报告全成功。重复调用 record 是新采集，不是发布重试。

编排结算采用一次同文件系统 rename。official.json 在启动 writer 前登记 mode、
runId/verificationId、fixture、staging 的 fixture 根相对位置和已知 material/execution
身份；在 rename 前写入精确目标及 settling 阶段，关闭 writer、剔除运行输入，并以
目标基准构造结论和库存。移动本次证据目录到 record/verify/失败域后校验封存结论，
最后清除活动登记。只有临时目录移动，不同时保留 staging 与目标副本；持久结果中的
引用从一开始就按目标包根构造；无 material 的失败域引用以 fixture 根为基准，
其 inventory 只能覆盖 failures/official-record/<runId>/ 前缀。verify 的 material 在开始时已固定；无 mock 时仍可在
该 package 下封存失败 verification，不能伪造 execution。

Open 只读检查明确登记的未完成事务并报告可用/中断状态，不隐式恢复或写盘；
下一次 action 在锁下执行恢复。仍有 writer 或不能证明进程
全部退出时返回 busy/cleanup 诊断，保留 journal 与唯一 staging，不能抢占或清理；
单个 PID 不存在不能证明整个进程组结束。可以证明全部 writer 关闭时，按 journal 的
ID/目标/哈希恢复：未提交 current 保持原选择，已提交 current 校验后完成索引收尾；
未完成 execution 标为 interrupted/failed，不重放 Agent、不补成功业务；staging 与
目标只允许一处存在，rename 后中断按目标库存完成结算。两处均存在、关联丢失或哈希
冲突时 fail closed 并保留证据，绝不按 latest 修复。已封存域只读，不因恢复重写。

runtime 的私有配置与可写宿主环境可在全部进程退出后删除；未结算的 staging 是唯一
失败证据，必须先完成上述结算，不能按“runtime 可丢弃”提前删除。删除整个 fixture
仍是调用方主动放弃其全部证据；工具不把这个动作伪装成结算成功。prepare/export 用
同父目录 staging 与原子 rename 发布，失败不留下可用半包；采用先校验所选闭包再
发布 attempt，最后原子替换固定索引。索引发布失败保留旧索引和已验证 attempt，按
相同 review/hash 幂等重试，不能覆盖不同内容或重复造 attempt。

### 框架路径与录制身份

框架持久化身份、哈希和根目录内的相对引用；执行时从调用方提供的 fixture 根目录
计算实际绝对路径。材料的身份不包含它在某台机器上的存放位置。
原始协议、原生历史、日志、用户提供的 plan 和参数保持原样，不执行路径字符串替换。

文件引用按 owner 域使用固定基准：fixture 控制文件相对 fixture 根目录，package
内的 execution、record 和 verification 证据相对 package 根目录，material 内部
引用相对 material 根目录。fixture 和私有归档维持相同 package 布局；最终 case 直接
发布 material 根，其内部引用和字节不变，不携带 package 证据域。
固定布局由 ID 推导，不重复保存路径；其余文件引用使用规范的 `/` 分隔相对路径。
读取前拒绝空引用、绝对引用、反斜杠、`.`/`..`、重复分隔符、非规范路径，
并逐级拒绝符号链接或非普通文件，防止逃逸声明根目录；不能先 clean 再接受。
引用中的 material/execution/run/verification/connection ID 必须与 owner 及被引用
对象一致；文件存在不能抵扣错 ID、缺失库存或未知版本。不引入通用
locator、路径占位符或对任意 JSON 字符串的替换机制。

| 字段或内容 | 持久化规则 | 运行时规则 |
| --- | --- | --- |
| material 的 `path`，connection/result 中的 material 路径 | 新材料不保存；控制面只保存 material ID 和内容哈希 | 从 fixture 的 `packages/<materialId>/material` 或最终 case 的 attempt 根定位 |
| fixture 的 `resourceRoot/workspace/home/config/tmp`，route 的 workspace | 固定布局不重复保存 | 根据当前 fixture 和 action 计算 |
| execution 的 `workspace/home/config/tmp/artifactRoot` | 固定布局不重复保存 | 根据当前 fixture、material ID、execution ID 和 action 计算 |
| fixture source seed 和非固定控制文件 | 保存 fixture 根相对引用及来源哈希 | 只访问 prepare 已复制并验证的 source 副本 |
| material 内的 history 和来源文件 | 保存 material 根相对引用及固定绑定映射 | 从所选 material 根读取，不查找原 seed 或旧 fixture |
| package 内的 record、verify、receipt 和 descriptor 引用 | 保存 package 根相对路径；文件哈希表使用同一基准 | 根据本次 fixture 或外部归档的 package 根读取 |
| launcher、框架命令中的 fixture 参数和目录环境变量 | 不保存可推导的本机位置 | 在启动前构造；工具身份仍须匹配 |
| `project/historyProject/_fixtureProject`、`native.cwd` 和原始参数 | 保留解释录制所需的身份与事实 | 不作为旧目录的文件读取入口，不替换为当前 workspace |

`protocolCwd` 是协议身份，不统一按 workspace 推导。record 和各 provider 的 mock
仍遵循各自合同；尤其 Codex mock 使用材料中的原始协议项目身份。`_fixtureProject`
所代表的录制工作目录继续用于解释已有历史和工具路径，不因它由框架生成而删除。
这些事实即使是绝对路径，也不要求对应目录存在。

新材料元数据不保存本机 `path`。已有材料中的该字段保留原字节，只作为旧格式事实，
不用于查找文件；加载器显式接收当前 package 的 material 根目录。导入不为更新位置
而重写旧 `material.json`、plan、manifest、历史或证据。fixture 的 current 指针与
材料文件分开保存；没有元数据的受支持旧材料，其导入身份保存在 fixture 控制面中，
不向旧 material 补写文件。

### 材料计划与运行配置

官方材料 plan 保存场景输入、实际使用的协议身份、原生参数、CLI 版本与哈希、
历史 seed 的 session 身份、文件绑定及来源哈希。它不缩减为原始 source plan 的副本：
录制准备阶段产生且回放或资格检查需要的事实必须进入材料闭包。
用户提供的 source plan 保持原字节并以哈希绑定。

新 official 与 consumer 都保存 `source-plan.json` 原字节，并从其生成唯一 material
`plan.json`。manifest.planSHA256、material.planSHA256、result 中的 planSHA256 均绑定
这个派生 plan；`sourcePlanSHA256` 绑定 source-plan 原件。official-origin 的 sourceSHA256
也绑定该原件，recordSourceSHA256 则绑定 prepare 冻结的 source 身份；不能混作同一个
hash。派生 plan 保留 source 未知字段和协议参数，只添加已定义的材料事实及 historyBindings，
去掉明确属于 runtime 的框架字段；用户 source 原件从不因此改写。consumer 的 plan 输入
version 1 不升版，material format 3 读者按上述新 plan/hash 关系解释，旧 consumer
material 的 planSHA256 仍按原格式绑定原始 plan，不追溯改写。

`root/home/config/raw`、`_seedRoot`、当前 driver workspace、结果文件位置和连接命令
属于本次运行配置，不进入新材料 plan。driver 分别接收材料 plan 和运行配置：
读取原始协议身份使用前者，访问本次文件和启动进程使用后者。材料中的
`_fixtureProject` 不再被当前 workspace 覆盖；`_recordedWorkspace` 不作为另一份
持久身份重复保存。已有材料中的旧运行字段只按旧格式读取，不能用于定位当前资源。

prepare 完整复制并校验所需 seed 来源闭包和项目快照；后续录制的来源一致性检查
对这份冻结副本执行。新 seed 不保存外部 `sourceRoot`，使用 fixture 内的明确引用。
来源 plan/manifest、历史文件及项目快照的哈希检查不能因去掉旧目录而省略，
也不能只保存哈希而漏掉需要复验的文件。已封存 seed 中的原项目身份保持不变。

history 在 fixture source 与 material 中各有一份不同生命周期的完整冻结来源闭包。
fixture 的 `source/history-seed/` 保存来源 plan/manifest、所需历史与项目快照的完整原树，
不只保存 selected files 或其 hash；material 的 `history/seed/` 保存本次使用的同一完整
来源闭包，供 export 后独立复验。不能要求 material 在原 fixture 删除后回读 source。

format 3 的唯一派生 `plan.json` 持有 `historyBindings` 表；不放在用户 source plan，
不再在 manifest.savedFiles/historySeed.sourceFiles 等处并列维护逻辑绑定。每条绑定明确
角色（seed/runtime）、原生 session/parent/child 身份、原始项目身份、native 目标相对路径、
material 根相对 source 路径与 SHA-256；SHA 必须与 manifest.files 的同一文件相等。
来源闭包的完整库存仅由 manifest.files 覆盖，绑定表只描述实际 materialization 的目标，
不得以绑定表仅选少量文件为由截断来源闭包。

035 的 `existing-children` 流程在一次 official record 内自行建立前置历史。
main 连接通过官方 SDK 执行 setup，结束真实父子任务并关闭后，agent record 保存
本次原生历史到 `history/seed/`；这份 seed 来自同一 execution，不引用其他 case
或外部资格包。`existing-children/setup.json` 与 material plan 的
`existingChildrenSetup` 绑定父子身份、setup raw/transport 哈希、历史绑定及完成时间。
probe 连接仅在本 execution 的 main 成功结算后启动，保存绑定 setup 哈希和开始时间的
`existing-children/observation.json`，在同一父身份上建立新的零输入观察入口。
Claude probe 的原生 argv 严格等于 setup argv 加 `--resume <parent UUID>`；
Codex probe 使用相同 argv，以原生 thread/resume 建立新观察。两连接各自的原始
协议与普通 receipt 均由公开 agent 命令拥有；probe 使用既有多连接的
`remote-probe/raw/` 布局，不新增官方专用 execution 路径。
官方 driver 分别校验 setup 创建、子完成、历史身份与正式观察发现；setup 失败不能
成为观察成功。后续 verify/mock 重放两个连接，并只读取 035 自己封存的材料。

`history/runtime/` 只保存录制完成时新增或已变化的原生历史。若某个声明的 runtime
目标与已保存 seed 文件身份相符且字节完全相同，绑定可以显式复用 seed 中那个文件；
不再存第二份物理文件。保留 native filename 与 session 身份，通过绑定让多个明确
目标读取同一文件；禁止跨无关历史按 hash 扫描去重、重命名为 hash、CAS 或通用 locator。
seed 与 runtime 各自有意义的不同字节必须同时保留，不能因为 sessionId 相同就覆盖。
material 内不保留并列 config/ 与 history-seed/ 历史树。

record/mock 把绑定内容复制到各自完整可写宿主 config/history 目标，不共享可写 hardlink。
Claude 必要的 cwd/project 派生只改允许的顶层身份，保留原 session ID、unknown fields、
数字字面量和原字节来源；不修改 raw/native 协议 init.cwd。materialization version 2
独立记录包内 source、必要 derived 字节、native 目标、sourceCWD/executionCWD、变换
类型与 sourceSHA256/derivedSHA256。必要派生字节只保存到本 execution 的
history/derived/，不复制整棵源树；运行时目标是可写副本，不是第三份持久来源。
materialization 完成、SDK 启动前校验 source 与实际派生目标的哈希；mock 返回前再
校验这些绑定文件未被修改。record 允许原生 Agent 更新其运行历史，按 history/runtime
保存变化后的新事实，不能把合法更新当成 source 被改写；冻结 source 仍必须重验。
归档读取方校验封存的启动时派生字节与变换关系，不重开旧 workspace。无派生时表明确为空，不伪造变换。

record 尚未有 core 时，用到的来源闭包随 capture 暂存；成功时移动进 core，再封存
materialization 的包根引用；失败时保留在 execution/capture/history-source/ 并按失败
域绑定。receipt 在封存前完成引用，封存后不能改写。mock 的来源只取 current core，
不得回退到 fixture source 或 route template。

执行流程如下：

1. prepare 导入并验证来源，保存唯一 fixture source seed、身份和相对引用。
2. record/mock 创建 execution overlay，按绑定把所需历史 materialize 到本次完整
   可写宿主环境，生成本次绝对 workspace、环境和启动命令。
3. record 原样采集协议和历史，分别保存唯一 material plan、规范 history 与执行证据；
   材料核心封闭并通过哈希校验后发布 current。
4. mock/official verify 从 current package 的 material 获取内容，从本次 execution
   获取运行位置；
   原始协议帧及严格匹配规则不变。
5. export 按 material 原字节复制为独立导出包；新目录中的 prepare 建立新的 fixture
   control 和 package 布局，不改写导入材料。

### SDK 宿主的运行时入口

公开 Go `Prepare`/`Open` 返回解析后的运行时 Fixture/Routes；进程内 mock 的精确启动
环境仍来自 `ExecutionStart`。CLI prepare 保留既有 stdout/退出码合同，不新增 open
命令，也不要求消费者从 fixture.json 重建路径。CLI 脚本在启动自己的 SDK 宿主前读取
私有 `runtime/routes/record.json` 或 `runtime/routes/mock.json`：这是 schemaVersion 1
的可重建 route 快照，包含 fixtureId、action、当前 materialId（record/空 current 可为空）、
workspace、protocolCwd、env、runtimeEnv 变量名、historyFiles 与 connection 命令/launcher。
`env` 仅包含工具生成的非秘密隔离值；认证来自宿主已经持有的 runtimeEnv 白名单或
私有配置，不能把当前进程整个环境写入快照。snapshot 中的绝对路径允许存在，因为它
只适用于当前 fixture 的活跃 runtime，不是持久定位证据。

同一 fixture 的调用方负责串行协调每次完整的 SDK 使用过程。协调区间从取得任何
route/快照或调用 `StartMockAgent` 之前开始，覆盖 SDK 宿主创建、启动前 history/config
发现、agent 领取 execution、业务执行，以及本次所有 SDK 宿主、peer 和相关子进程
停止、输出结算与 route 恢复；这些步骤完成后才允许下一次独立使用。agent action
返回或单连接完成都不代表宿主已停止。该责任同时适用于普通 Go/CLI 消费者与调用
官方编排的外层调用方；official 命令负责自己启动的 driver/SDK/peer 收尾，不能替
外层调用方协调另一个消费者。可用调用顺序或调用方自己的进程协调保证该区间；
无法共同协调或需要独立并行时使用不同 fixture。

同一 execution 声明的 main/probe 等连接属于同一次已协调使用，可以有多个 SDK
宿主/peer 并发，共享上述协调区间；不得把另一独立使用加入其中。协调期间不持有
会妨碍其 agent 子进程领取/join execution 的框架锁，不新增 token、lease 或命令。
`StartMockAgent` 即使先领取 execution 再启动 SDK，也不免除整个宿主生命周期的
调用方协调责任。

prepare、current 提交后的 route 刷新和 execution 路由恢复负责原子刷新快照。读者
仍须核对 fixtureId/action、current ID 和 runtime 是否存在，已知过期或缺失时失败；
这些检查与读取快照本身没有互斥或锁语义，字段一致不证明没有活动 execution，也
不能消除检查后到 agent claim 之间的竞争。框架只在正常 agent claim/official 活动
登记时按既有锁检测冲突并返回 busy；这不保护 claim 前 SDK 对可写 template/history
的访问，不承诺拦截未经协调的宿主启动或防止这种误用的污染。不得增加非原子 busy
字段来声称补足保证，也不能自行改写快照或凭旧值绕过调用方协调。
提交后快照刷新失败属于已提交后的 cleanup 失败，恢复必须按 current 重建快照。
Go 运行时对象与快照使用同一 route 解析器，避免独立路径算法；StartMockAgent 仍
返回实际 execution 的绝对路径，不把启动前 template 快照冒充 execution 环境。
route template 必须在 SDK 启动前提供发现历史所需的完整环境；本文不保证旧 cwd
与 SDK 已冻结身份在后续 overlay 切换时自动等同，不声称解决 session_cwd_mismatch。

### 路径相关证据

录制隔离和当前验证隔离是两份不同执行的事实。录制时在真实执行环境中检查目录归属、
有效环境、启动参数和进程身份；新证据以 execution 身份、相对引用及哈希绑定结果。
归档后的资格检查验证这组封存证据及其关联，不用当前 verify 的 overlay 证明过去
录制正确。当前 verify 的隔离由它实际调用的 mock execution 独立证明。
已观察到的绝对 cwd、argv 可以保留为采集事实，但不能作为重开旧目录的依据。

旧录制证据按其原版本校验身份、哈希和路径关系，不访问旧目录，也不因含绝对路径而
自动拒绝。缺失证据、未知版本、关系冲突或哈希错误仍然失败；不能通过替换为当前
目录、只比较规范化哈希或忽略字段来接受材料。

CLI 结果通知与落盘 result 使用同一对象、同一 package 相对引用和同一证据版本，
不在通知端与落盘端做两套路径转换。running 通知同时给出 material ID，
因此 execution 创建后即可定位 `<fixture>/packages/<materialId>/`；消费者把
package 相对引用解析到该包根。fixture control 的包选择仍由 fixture 根和
material ID 推导，material 内部引用仍相对 material 根。私有归档使用相同 package
相对引用；最终入仓 material 不包含旧 execution 或编排结果引用。
公开 Go 运行时对象中的路径字段继续返回当前解析后的绝对值，字段含义不变；
这些对象不作为原始持久证据重新序列化或参与原字节哈希比较。

可移植性通过 export 后在新目录重新 prepare 验收。直接移动整个已准备 fixture
并继续运行不属于此合同；route 符号链接、launcher 和活动执行不因此获得搬迁保证。
原始回放 `init.cwd` 不变，SDK 对旧 cwd 与当前物理目录的身份校验不在此设计中改写。

## schema 版本与兼容

`fixture.json`、`execution.json`、execution index 和 `current.json`
是同一个 fixture 控制面合同，共用一个 `schemaVersion`。这些文件在 prepare、
record、mock 和切换 current 时一起写入并相互引用，分别版本会产生无法判定的混合
状态。material/manifest 保留自身格式版本；进程 receipt、官方编排和 verification
中的 `schemaVersion` 属于各自证据合同，不与 fixture 控制面版本比较。

| 合同 | 当前版本 | 兼容边界 |
| --- | --- | --- |
| fixture control 与新 execution descriptor（`fixture.json`、`current.json`、execution index、`official.json`、`execution.json`） | `schemaVersion: 7` | schema 6 及更旧 fixture 拒绝并重新 prepare；无迁移和混合读取 |
| material（`material.json` 及其核心布局） | `formatVersion: 3` | 无该字段的旧格式和 format 2 保持完整只读兼容 |
| official record/verify、native preparation/isolation | `schemaVersion: 3` | 域 inventory、引用基准及 plan 关联变化；1/2 按已定义旧合同只读 |
| official-origin.json | 保留既有 origin 字段合同（无独立 schemaVersion） | material format 3 只引用这个唯一对象，不再内嵌副本 |
| 逐连接 origin proof | `schemaVersion: 2` | schema 1 的内嵌 origin 改为 originSHA256；原采集 PID、身份语义不变 |
| history materialization | `version: 2` | version 1 的运行树引用改为包内源/派生字节引用，并增加 native 目标绑定；旧版按原格式只读 |
| mock 协议 receipt | `schemaVersion: 2`；`version: 1` 回放结果语义不变 | schema 1 的 recording 是 fixture 根相对引用；schema 2 改为包根的 material/，旧 receipt 按原合同只读 |
| native recording/raw manifest、process receipt | 保留各自已有版本 | 原始帧和采集文件不改字节；process receipt 的 tool/cwd 是观察事实，无新文件定位语义 |
| execution 根汇总 | `schemaVersion: 1` | 新增对象，定义状态、inventory 和发布事实；不借用 control schema 7 或 agent result schema 3 |
| agent result 与 CLI 通知 | `schemaVersion: 3` | schema 2 为 fixture 根相对引用；新版本增加必填 materialId 并改为包根相对引用，旧对象只读，不能混用 |
| 私有归档的 `review.json`、`adoption.json` | `schemaVersion: 2` | version 1 只读兼容，不重写旧证明文件 |
| official driver 运行配置 | `schemaVersion: 1` | 与 material plan 分开；不识别该版本的 driver 在启动前失败 |
| CLI 宿主 route 快照（仅 runtime） | `schemaVersion: 1` | 新的短生命周期读接口，不与 driver runtime 1 或控制面版本混判；删除 runtime 后不可使用 |

控制面使用 schema 7。新 `material.json` 使用 `formatVersion: 3`，不含 `path`，
并绑定 package 内唯一 material plan 与 canonical history。material core 在成功
record 发布后不再改变；同一 package 的后续 record 报告与 verification 只能在
独立证据域新增文件，不能更新 material manifest 来登记新证据。

结果通知与持久 result 使用 schema 3，不能把 schema 2 的 fixture 相对路径静默解释为
package 相对路径。新的 official record/verify、native preparation/isolation 使用 schema 3。
mock receipt 的 recording 引用基准变化，故 schema 1 升为 2，内部回放结果 version 1 不变。
material 根 manifest 的完整库存语义由 format 3 定义；`raw/manifest.json` 的原生格式不变。
origin 的唯一性由 material format 3 的引用结构表达；逐连接 proof 删除内嵌对象，故单独
从 schema 1 升为 2。materialization 的 source/derived 改为包内封存文件引用，native
target 单列为逻辑运行目标，故从 version 1 升为 2，不套用 isolation 的版本。各域版本独立判断，不从目录名或“最新”文件猜测 version。

持久化语义发生不兼容变化时才升版。以下任一变化必须升版：

- 删除或重命名字段，改变类型、单位、编码、必填性或缺失时的默认语义。
- 改变现有字段、身份、哈希、路径、状态或文件间关联的含义。
- 新增字段后旧版 reader 不能忽略该字段，或新版 reader 无法继续读取旧数据。
- 改变严格回放、failure class、证据归属等会影响既有持久化结论解释的规则。

仅修改注释、错误文案或内部实现，或者新增旧版 reader 可以忽略、缺失时有明确默认值
的可选字段，不升版。版本号单调递增，每次不兼容变更只加一，不复用已发布的值。
一次连贯的破坏性变更只升一次；不能为尚未发布的多个字段分批升版，也不能保留中间
格式。版本号、兼容判断、相关规格和拒绝旧版的测试必须在同一个变更中提交。

兼容交付先提供 reader/validator，再启用 writer，最后接入采用与快照生成：

1. 读者同时按明确格式识别旧 core/legacy 证据与新 package；旧 data 的检查仍走旧合同，
   不重新序列化或升级原文件。新格式的未知/混合字段和版本在执行前失败。
2. 新 prepare/agent/official 统一写 control 7、material 3 及表中对应域版本，不能写
   新目录却沿用 schema 2 通知。未升级的 executable/driver 通过身份和版本检查拒绝，
   不在同一 fixture 混跑新旧 writer；driver runtime 1 的请求结构保持不变。
3. qualification/adoption/builtin 只按 review 选择的域读取库存；新采用使用新布局，
   旧采用仍可读但不自动改树。新旧读取和拒绝路径均达到离线接缝验收后才能启用发布。

读取持久化合同前先校验版本。fixture 控制面只接受当前 `schemaVersion`；更旧和更新
的版本都在执行前失败，不按当前字段猜测、不迁移、不双读，也不接受混合版本目录。
fixture 由调用方拥有且可重新 prepare，因此控制面的兼容窗口为零：升级后重新
prepare；需要保留的材料先用旧工具 export，再用 `prepare --recording` 导入新
fixture。material/manifest 的旧格式按自身合同只读处理，不因控制面升版而改写。
归档 execution descriptor 可以按其旧版本读取证据；它不参与新 fixture 的 Open、
调度或 join，因此不构成新控制面的混合版本。
已有 `data/` 的 review、adoption、official replay 和 raw 对象保持原字节与 Git
object 身份；新 reader 读取旧布局不等于迁移或重写。新入仓 case 才使用当前
package 和 review/adoption 合同。
`schemaVersion` 不是工具版本、协议版本或 SDK 版本，不能互相替代。

## 记录、验证与回放

### agent record

agent record 是 SDK 启动的录制代理。它使用 prepare 提供的录制来源和隔离环境，
创建 execution，启动或连接真实 Agent，转发双向协议并保存原始帧、stderr、plan、
manifest、历史快照、采集和清理结果。它不启动官方 driver，也不编排 SDK 业务。
官方 driver 与消费者 SDK 宿主都使用这一公开进程接口。

record 成功要求本次采集、材料闭包和清理达到自身合同。成功后生成不可变 material，
并原子替换 current recording。package 只以该 material id 命名，
execution id 独立；
失败或不完整的采集保留部分 package、execution、stderr 和已有帧，不封存成功 material；
提交前失败不改变 current，提交后交付失败按封存合同明确报告。record 不执行官方验证、资格判断或采用，新材料不继承旧验证证据。

record 不等待父 driver 的最终业务报告，也不以业务断言结果决定采集是否成功。
material 在采集完成时封闭；driver 随后生成的业务报告保存在官方编排记录中，通过
execution ID、material ID 和哈希关联，不回写已发布 material。采集通过与官方
业务通过分别记录；后续业务失败不撤销已经完成的采集，也不能把编排结果标成成功。

record source 与 current recording 分离：每次 record 使用 prepare 固化的同一
plan、协议 cwd 和来源身份。缺少 plan、协议 cwd、目标或必要参数时，在领取
execution 前失败。plan source 的消费者流程见[消费者录制](consumer-recording.md)。

### official record

official record 选择当前 fixture 的精确官方 case/binding 和 driver，准备 SDK
宿主环境并启动 driver；driver 通过官方 SDK 启动公开 agent record，后者才负责
真实 Agent 和录制 execution。official record 只持有编排状态、driver 进程及业务
事实，不创建自己的协议 execution 或第二套 overlay。消费者 plan 无适用官方
录制 driver 时，在启动 driver 前失败；消费者仍可直接使用 agent record。

official record 向 driver 提供 agent record 的命令、真实 Agent 目标和参数；driver
必须使用该命令，不得直接绕过录制代理启动真实 Agent。official record 保存实际
SDK/driver 身份、业务报告、退出状态及实际 record execution/material 的精确引用。
SDK 宿主应用 agent record 的正常环境合同。启动失败、未触发、业务失败和采集失败
分别保留事实，不把 driver 成功退出等同于录制完整。

driver 的结果通过普通 launcher result fd 返回 material、execution 和
connection ID，official record 不从目录时间或“最新 package”推断归属。driver 的
临时输出先位于 runtime；结算时只把本次 `record/<runId>/` 发布到已由 agent record
创建的 package，并引用其中的精确 execution。材料发布后父 driver 才失败时，
current 与 material 不回滚，`record/<runId>/result.json` 记录失败结论且不能入仓。
在 driver 尚未触发 agent record 时，不伪造 package、material 或 record 域。

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
3. 启动 driver，由 driver 使用官方 SDK 调用公开 `agent mock --fixture-dir ...`；需要
   多连接时使用材料声明的连接参数。mock 自己创建和结束 execution、管理 overlay、
   连接、receipt 和清理，并把 execution 直接写入 current package；verify 不预分配
   这些对象，也不覆盖 mock 的材料选择。
4. 保存 driver 的业务报告、实际 SDK 身份、退出状态和诊断，核对本次实际调用的
   mock execution、连接 receipt 和进程退出事实，在同一个 package 新建唯一的
   `verify/<verificationId>/`。

driver 必须明确报告实际调用对应的 execution ID 和连接 ID；verify 核对这些引用
属于当前 fixture、绑定本次固定 material，且覆盖所需的全部连接。不能通过扫描
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
mock execution 中，verification 引用并核验其内容哈希，不复制第二份 receipt。
`verification.json` 每个域只保留一个权威结论和一个完整可校验 inventory；
`result.json` 与 `replay-report.json` 保存 driver 执行事实，不把内嵌报告展开复制。
同一 package 可以追加多个独立 verification；每个验证绑定本次 execution 和连接，
不扫描 latest。current recording 被新的 record 替换后，旧 verification 仍属于旧
package/material，但不能证明新 current；verify 结论不能转移或继承。

verify 默认只产出证据。只有显式要求写回仓库时，才在同一精确验证结论上执行资格
检查、私有归档和原子采用；采用失败保留验证证据、旧索引和旧材料。

### agent mock

mock 只把 current recording 作为原生协议对端。它不检查 current 是否 verified，
也不要求 mock 之前执行过 verify。没有 current recording、材料格式不兼容或连接
声明不可用时，mock 在领取 execution 前失败，不能产生空成功。

普通 mock 和 official mock 都把 execution 直接写入 current package 的
`executions/<executionId>/`，使用相同的 descriptor、isolation、prepared plan、
receipt、finalize、索引和 fail-closed 校验。普通 mock 不创建 `verify/` 域，也不
需要 official verifier；official verify 只引用 mock 已经产生的 execution，不在
自己的目录生成第二套连接或 receipt。mock 失败仍保留该 package 内的执行证据，
current 指针和 material 不变。

mock 不执行录制中的 shell、文件编辑、hooks 或模型调用，也不重建其业务副作用。
它不模拟 Agent CLI 的版本、帮助或其他元命令。消费者负责启动 SDK 宿主、应用
prepare 返回的环境和 workspace，并自行执行与报告业务断言。

库消费者使用 `StartMockAgentStreams` 提供 `Reader`、`Writer` 和诊断 `Stderr`。
`Reader` 是 SDK 到 mock 的读取端，`Writer` 是 mock 到 SDK 的写入端；关闭任一端都必须
解除相应的并发阻塞 I/O。正常完成时 mock 在最终检查后关闭 `Writer`；abort 或
transport failure 时关闭相关方向。`Stderr` 只承载诊断，不参与协议、完成或取消。

每次 mock 创建新 execution。普通流程只有 `main` 连接；只有 case 确实声明多个独立
原生客户端生命周期时才允许多连接。同一 execution 的连接可以并发，但每个声明连接
最多启动一次，并分别完成严格匹配、完整消费、EOF、退出和清理。

replay 在服务端帧、pending request 检查和握手 barrier 之后、额外客户端字节和 EOF
检查之前触发完整消费事件。该事件只允许 SDK 半关闭输入，不表示成功；完整消费到达前
失败时事件不触发，但 `Done`/`Wait` 必须结束。正常完成、canceled、aborted、transport、
strict-replay、timeout 和 cleanup 使用不同终态；`Wait` 是权威完成结果，提前收到
`Writer` EOF 不能替代它。

mock 结果分别给出本次连接状态和整个 execution 状态；连接 `passed` 不代表其他声明
连接已经完成。mock 进程 receipt 不记录 `cwd` 或 `workspace`；执行位置和协议项目
身份以 execution 身份解析出的 workspace 和明确的 `protocolCwd` 为准。为兼容 v1，
校验器忽略旧 receipt 中可能存在的这两个字段，回放和历史文件始终从 fixture/execution
根定位。

库 mock 的 `ExecutionReport` 覆盖 execution 声明的全部连接，并从 descriptor、
index、receipt 和 result 重新校验生成。进程内连接记录 `transport=in-process`、
`nativeLaunched=false` 和空 `exitCode`，不得伪造 PID、executable 或进程退出。
报告依赖的事实缺失或哈希不一致时 fail closed。完整 API 见
[可导入 Go 库入口规格](library-entry-spec.md#启动-handle)。

官方 driver 和 CLI 消费者使用公开 agent mock 进程接口，库消费者使用
`StartMockAgent`；两者共用同一 execution、回放和 receipt 合同。official verify
仍通过公开 agent mock 进程入口工作，不直接调用协议回放函数替代它。

### 远程录制与命令边界

agent record 内部提供 stdio 与认证 WebSocket 的双向桥接，不提供独立 connect
命令。目标由调用方显式声明为本地 executable/argv 或已有远程 endpoint，两者
互斥；远程认证通过私有运行文件传入，不进入 fixture 的持久证据。official record
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

export 把 current recording 中封闭的 material 核心原字节复制到 fixture 外：

- 包含 material identity、plan、manifest、raw、绑定历史和来源哈希。
- 不包含整个 fixture、control、其他 package、execution、record、verification 或 runtime。
- 不产生 verified、admission eligibility 或 admitted 结论。
- 不修改 current recording、package、execution index 或 fixture 状态。

目标目录必须位于 fixture 外且尚不存在。工具先写入同父目录下的临时目录，校验副本
哈希，再原子 rename 为目标目录。导出根就是 material 根，不额外套一层 package
目录。export 可以重复执行；不同目标互不覆盖。导出包可以作为下一次
`prepare --recording` 的输入。

只要 fixture 中的材料仍存在且哈希有效，就可以重复 export。legacy 的“core”就是
按原合同导入的完整旧树；其中原有内嵌 official-replay/review/adoption 不被剥离或
重排。export 不添加新 package 的旁挂证据，也不以删除旧证据来伪造更小的新 core。
完整证据包的私有归档与 material 发布、core export 是不同边界：归档依 review 选择
完整已封存域，发布和导出只复制 core。只导入 standalone core 缺少原 record 业务证据时可以
mock/export，不能仅凭新的 verify 重建原采集通过或取得采用资格。

## 信任与入仓

工具区分四层结论：

| 层级 | 含义 | 产生方式 |
| --- | --- | --- |
| Action success | 某次 agent 协议执行或 official 编排按自身合同完成 | agent execution 或官方编排结果 |
| Verification passed | 精确 material 通过官方 verify | verification 引用的 driver 事实与 mock execution 证据 |
| Admission eligibility | 材料具备进入默认 case 库的资格 | 显式资格检查 |
| Admitted | 材料已归档并写入默认 case 库 | 显式采用 |

- record 的采集/发布事实与结果交付分别记录，不表示官方业务验证通过。
- official record 成功要求采集与官方业务事实均完整，不表示已通过 official verify 或采用。
- verify 通过只对绑定的 material 哈希有效，不代表已入仓或允许公开发布。
- mock 成功只表示协议回放、退出和清理完成；消费者业务结果由消费者报告。
- export 成功只表示字节复制和哈希校验完成，不提升任何可信等级。

只有同时满足以下条件的 material 才具备默认 case 库的入仓资格：

- 身份是精确的官方 case 和 agent binding。
- provenance 为 official。
- 官方录制业务事实完整、通过并与精确 material 关联；已有材料内的原生业务证据
  保留原字节，新增编排记录通过身份和哈希绑定，不以缺少事实推断成功。
- 对该精确 material 的官方 verify 通过。
- 资格检查确认来源、录制与验证隔离、业务结果、material 闭包和哈希均有效。
- review 明确选择 material ID 对应的 package、record run、verification 及其引用的
  execution；证据闭包包含全部声明连接和所需 receipt，而不是扫描最新目录或复制
  整个 record 树。

资格判断和采用都不能由 record、mock 或 export 隐式触发。采用是显式、
原子、可重试的仓库写入；写入前保留旧索引，写入失败不删除已验证材料。
采用先把 review 选择的完整封存闭包及证明文件保存到私有归档，再把 material 原字节
原子发布到最终 attempt 根并写回索引。最终 attempt 不包含旧 execution、record、
verify、review、adoption、control 或 runtime。索引保存材料描述文件哈希、归档采用
证明哈希、官方 SDK 身份和资格方法/时间；内置生成检查材料完整性与身份，不重新打开
外部证据。归档失败、证据缺失或资格检查失败不能发布。原始 plan/raw、material 内
来源与历史绑定保持不变。用于后续真实录制的已合格历史种子仍须来自完整证据包，
不能仅凭已发布 core 或索引摘要推断录制业务通过。

consumer material 和外部用户自行保存的录制可以进入自己的 fixture、由用户自己的
验证流程评价并通过 export 保存，但不能冒充官方 case 验证，也不进入默认 case
库或内置快照。

## 不变量

- prepare 是外部材料进入 fixture 的唯一入口；prepare 完成后 action 不再读取原始
  外部材料路径。
- prepare 完整复制并校验副本；删除 prepare 的原路径不能影响后续 verify、mock 或
  export。
- 一次 prepare 可以承载多个 action execution；source 类型不限制可调用的业务动作。
- fixture 执行与验证的并发规则见[多个 action execution](#多个-action-execution)；
  verify 必须允许其 driver 调用公开 mock。
- 每个 action execution 有唯一 id、独立 overlay、领取时冻结的 descriptor 与最终封存的 receipt/result。
- current 的提交与失败恢复见[封存、发布与恢复](#封存发布与恢复)；不能把尚未封存
  的 material 当作成功材料。
- current recording 始终指向一个完整、不可变且哈希有效的 package/material，或
  保持为空；package 只以 material id 命名，不存在第二个持久别名。
- material core 封闭后不可变；后续 record run 和 verification 只在 package 的
  独立证据域新增文件，不修改 material manifest 或历史绑定。
- fixture package 与私有归档使用相同的证据域布局；仓库和内置快照只发布原字节
  material，回放不读取外部归档。
- verify 只读 current package 的 material，不修改材料；verification 绑定精确
  material 哈希和本次 mock execution。
- 新 record 不继承旧 verification，旧 verification 不自动适用于新 current。
- mock 不检查 verified，也不要求 verify 先于 mock。
- verify 不拥有 execution 或协议连接；缺少 current recording 或适用官方 verifier 时，
  在启动 driver 前失败。
- mock 缺少 current recording、材料格式不兼容或连接声明不可用时，在领取 execution
  前失败。
- 库 `StartMockAgent` 必须使用与 CLI mock 相同的 execution、严格回放和 finalize；
  进程内连接不得伪造 executable、PID、launcher 或退出码。
- 普通 mock 与 official mock 使用同一 execution 存储和校验合同；official verify
  不创建第二套协议 execution、连接或 receipt。
- fixture、execution 和 index 使用 schema 7；material 使用 format 3；embedded
  工具身份不得把 module path 当作 command，schema 6 及更旧 fixture 明确拒绝。
- export 不改变 fixture，不产生可信结论，目标必须在 fixture 外且不能覆盖已有目录。
- 只有精确官方 case material 的 verify 通过可以作为入仓资格的一部分；mock 成功、
  export 成功不能替代。

## 失败与证据

record 和 mock 的失败分为领取 execution 前后两类：

- **领取前失败**：fixture 不存在或忙碌；record 没有可执行的 record source；
  mock 没有 current recording
  或兼容的连接声明。工具不创建 execution，直接返回诊断。
- **领取后失败**：真实 Agent、采集传输或回放连接在执行中失败。工具必须保留
  execution descriptor、已产生的原始帧、stderr、连接 receipt、进程状态和清理结果，
  并按统一 finalize 路径结束 execution。

领取后失败还必须记录机器可读的失败类别，至少区分 canceled、aborted、transport、
strict-replay、timeout、cleanup 和 internal。`FullConsumption` 已触发、`ClientEOF`
为 true 或连接 receipt 看起来正常都不能覆盖这些失败；abort 不得伪造客户端 EOF。
库 `Wait` 与持久 execution index 的结论一致，并在返回前完成 replay/finalize
goroutine 收尾。

verify 前置检查失败直接返回诊断；开始验证后，无论 driver 启动失败、业务失败、
超时、取消或缺少 mock 证据，都在 verification 中保留已知事实和失败原因。
未产生 mock execution 时明确记录未启动，不补造 execution。已产生的 mock
execution 按 mock 自己的 finalize 合同保留；verification 写入失败不能改变其结论。

official record 同样保留 driver 启动、业务、退出和取消事实，引用已经产生的
record execution/material；确认未启动与关联未知分别记录。失败证据的归属只按
[多个 action execution](#多个-action-execution)中的编排输出规则结算或保留未结算事实。
driver 在采集发布后失败，
record run 为失败，但已发布材料及其 current 指针不因业务失败回滚。等待子进程退出
后才能结算 staging 并结束编排登记；未清理完成不能报告成功。

record 提交前失败时，current recording 不变；提交后失败保留提交事实；verify 失败时 current recording 不变；
mock 失败时 current recording 不变。失败 attempt 的部分 package、execution 和
record/verify 域保留供诊断；只有完整、成功且被 review 选择的证据闭包可以资格归档，
对应 material 才可入仓。
任何失败都不能删掉旧材料或旧验证证据。

工具按[封存、发布与恢复](#封存发布与恢复)区分采集、current 提交、通知交付与编排结算；
不能把多文件更新描述成一个原子事务。official driver 的临时输出先
位于 runtime，结算后只发布为 package 相对路径；持久证据不能指向已删除的 runtime。
verification 在 driver 和 mock 的事实落盘后写入自身结论，之后才可显式采用。
索引更新失败时，未索引的 execution 或 package 保持可诊断状态，不能伪装成成功。

凭证、认证变量和原生私有配置只写入 fixture/runtime 中的私有运行文件，不进入
fixture.json、material、execution、官方编排记录、verification 或 export 包。
远程 token 文件可以位于 runtime 或 fixture 外，不能位于 fixture 的持久证据区。

## 环境与隔离

prepare 为 fixture 创建独立环境基线，包括 workspace、HOME、config、tmp、历史种子
和连接槽。每个 record/mock execution 从基线派生可写 overlay，一个 execution 的副作用不能进入
另一个 execution。Codex 使用 `CODEX_HOME`，Claude 使用 `CLAUDE_CONFIG_DIR`。
模板或 route 宿主目录是启动前完整可写环境，可以包含必要 baseline 文件，但不能
保存整包 seed 的第二份副本；历史文件按冻结 seed 的明确绑定 materialize。

库 mock 的启动环境必须来自本次 execution 的 `ExecutionStart`，不能使用 prepare 的
route template。Codex transport 的 `Dir` 使用 `ProtocolCwd`，它只是逻辑协议项目身份，
禁止通过 `os.Chdir` 改变宿主 cwd；Claude transport 的 `WorkDir` 使用 `Workspace`，
同时作为原生 cwd 和历史 discovery 位置。两者都使用合并后的有效 `Env` 作为完整替换
环境，不能修改 `os.Environ` 或调用全局 `os.Setenv`。

认证只来自工具允许的原生配置和当前进程中的白名单变量。工具不复制个人 skills、
插件、记忆、项目指令或无关 session；不把完整 Agent home 当作 fixture 数据。

plan source 的消费者必须在启动自己的 SDK 宿主前应用当前 route 的 env、workspace、
历史和连接命令；库 mock 消费者则使用 `StartMockAgent` 返回的 `ExecutionStart`。
只配置 record 或 mock 子进程不能配置父进程。消费者不能覆盖隔离路径或原生环境变量。

record 完成时把回放所需的原生 session/history 写入 material 的
`history/runtime/`，把本次使用的冻结 seed 闭包写入 `history/seed/`，并由唯一
material plan 记录来源、逻辑目标、物理路径和哈希。官方验证和消费者在 mock
execution overlay 中产生的宿主状态不会自动提升为新的 material。删除 runtime
不影响材料导出；删除整个 fixture 前需要先导出待保留材料。

## Case 与 agent type 配置

[场景目录](../data/scenarios)保存用户故事、case、输入、预期和 agent binding。
case 身份、输入顺序、required/optional、`live-selected-path`、`planned` 和
`unsupported` 都是权威数据；人工构造的验证输入不能替代真实使用流程。

`--agent codex|claude` 在 source 能唯一确定时可省略：plan 从自身声明推断，
recording 从 manifest 推断，case 只有在只有一个可用 binding 时才自动选择；存在
多个 binding 时必须显式给出 `--agent`。显式值与推断值不一致时 prepare 失败。

外部 plan 的 caseId 是消费者自定义身份，不是官方 catalog case。它参与自身 fixture
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

完整消费事件只在这批服务端帧和已到达请求完成 pending request 检查、必要握手
barrier 之后，以及额外客户端字节/EOF 检查之前触发。它允许 SDK 半关闭输入，但不
表示 EOF、cleanup 或 execution 成功；提前 EOF、额外字节、超时、取消和清理失败分别
记录，不能互相覆盖。SDK 因主动半关闭产生的本地写错误不是 replay transport failure。

mock 不支持的分支或协议明确失败，不搜索其他 material、不猜测 agent type、不启动
真实 Agent。consumer material 使用同一回放引擎，不会因为来源是消费者而增加通配、
字段忽略或 UUID 自动生成规则。

## 测试边界

完整流程验收通过公开 Go 库入口和 installed CLI 的消费者接口验证 fixture 合同，不直接
调用 lifecycle 函数；测试分层和目录遵循[开发规范](development.md#测试)。至少覆盖：

- prepare 完整复制材料后，删除原始路径仍可 verify、mock 和 export。
- 对同一受控场景比较完整归档与 fixture 的所选证据域原字节，以及已发布 material
  与原 core 的完整哈希清单；删除外部归档后，入库材料仍可独立 prepare/mock/export。
- export 后删除原 fixture、fixture source seed 和外部 seed，在新目录 prepare 后
  仍可 mock、适用的 official verify 和再次 export；不以直接移动 fixture 替代。
- 导入、验证和导出保持旧材料全部原文件的字节及哈希，不为修正路径重写元数据。
- 已有 `data/` 的 Git 对象和原文件字节保持不变；旧 material、evidence 1/2 和
  review/adoption 1 仍可按原版本只读校验。
- 新控制面及材料中的定位字段不保存本机绝对目录；原始录制身份和采集事实单独校验，
  不按任意字符串中是否出现绝对路径判断材料合格。
- CLI 通知与落盘 result 完全一致；Go 运行时路径正确解析，相对引用越界和错误
  execution/material 关联必须失败。
- 录制隔离证据和当前验证隔离分别验收；丢失原录制证据不能由当前正确的 overlay 抵扣。
- record 成功封闭 material 并原子替换 current package；提交前失败保留部分
  package/execution 且 current 不变，提交后通知失败保留 publicationCommitted 事实。多次成功 record 产生独立 package。
- 每次 official record 有独立 runId，实际触发采集后才有独立 execution/package；
  同一 package 可追加多个 verification；失败 attempt 保留但不可入仓，
  每个成功域都有独立 ID、结论和完整 inventory。
- verify 只作用于 current，并在新 record 后不把旧 verification 转给新材料。
- 没有 verified 的 current material 可以正常 mock。
- verify 不创建 execution；driver 必须调用公开 mock，由 mock 管理 execution。
- verification 精确引用本次实际启动的 mock execution，不能采用其他调用的成功证据。
- 普通 mock 与 official mock 在 package 中产生同构 execution、isolation、
  prepared-plan、receipt 和 result；普通 mock 不生成 verify 域。
- 验证期间 record 和重复 verify 失败，但 driver 的公开 mock 不被 fixture 锁阻塞。
- 同一 fixture 的 record/mock execution 串行；同一 execution 的多连接可以并发。
- 没有 current 或适用 verifier 时，verify 在启动 driver 前失败。
- 没有 current 或连接声明不可用时，mock 在领取 execution 前失败。
- 库 mock 用 `io.Pipe` 或等价可关闭流完成 prepare → `StartMockAgent` → SDK
  `CloseInput` → `Wait`，测试环境没有 `agent-sdk-testify` 可执行文件或 PATH 发现。
- `MockRun.Start` 来自实际 execution overlay，包含当前 workspace、protocol cwd、
  env 和 connection；调用前后宿主 cwd 与全局环境不变。
- `FullConsumption` 在 pending request/握手之后、额外字节/EOF 之前触发且不丢信号；
  SDK 未启动、提前取消或 abort 时事件不触发，但 `Done`/`Wait` 结束且不伪造 EOF。
- 正常、canceled、aborted、transport、strict-replay、timeout 和 cleanup 分别记录
  失败类别；重复 `CloseInput` 使用 `ErrInputClosed`，半关闭在途写错误不是 transport。
- 库多连接通过显式 execution id 加入；CLI 按当前 index 的声明连接合同领取；`ExecutionReport` 覆盖全部声明连接，进程内
  连接没有伪造的 PID、executable 或退出码。
- Codex 使用 `ProtocolCwd` 作为逻辑 transport cwd，Claude 使用 `Workspace`；
  两者使用 `ExecutionStart.Env` 替换环境，不修改全局环境。
- schema 6 及更旧 fixture 被拒绝，embedded fixture 不含 module-path-as-command；executable
  fixture 的进程 mock 路径继续通过 CLI descriptor 验收。
- 错误 material/execution/run/verification/connection ID、错误 hash、绝对或
  `..` 路径、缺失证据和未知版本全部 fail closed；篡改 record 证据与篡改 verify
  证据分别失败，不能互相抵扣。
- driver 启动失败、提前退出、验证取消和 mock 缺失时保留验证事实；未完成的验证
  不能产生成功结论或允许清理仍在使用的资源。
- 旧顶层 record/mock/verify 和整个 internal-protocol 入口被拒绝。
- official record → driver → agent record 与 official verify → driver → agent mock
  都通过真实 installed CLI 接口；agent record 绝不再次启动 driver。
- driver 最终业务报告与 material 分开保存；采集成功后业务失败只使 record run
  失败，不篡改 material 或 current。
- agent record 的远程目标保留认证、双向采集、EOF、取消和连接关闭证据，不终止
  调用方已有服务，也不启动本地 Agent。
- mock 不响应 Agent CLI 版本或元命令。
- export 可重复、不改变 fixture、不产生入仓结论，导出包可再次 prepare。
- 最终入仓只复制 review 选择的精确闭包和原字节，保留相对路径；不复制 control、
  runtime、fixture source seed、未选 package 或整棵 record 树，且不留下悬空 runtime 引用。
- runtime 全部位于 fixture 内；调用方删除 fixture 后没有遗留运行目录。
- 删除 runtime 后仍能 export 材料；export 包不包含 runtime 或私有配置。
- 空的 stdout/stderr 或 `stderr.raw` 仍作为零输出证据保留，不能按 0 字节删除。
- Claude 源历史与派生历史在 mock 返回前校验，失败写入该 action 结果。
- 只有精确官方 case material 的 verify 通过才具备入仓资格；consumer material 和
  未验证材料不能进入默认 case 库。

布局合同的最高公开接缝映射如下；内部单元测试只枚举本层的路径/哈希组合，不复制完整
流程用例。受控录制进程、synthetic 材料和离线 driver 可用于工具验收，不声称官方 SDK
或真实录制已通过。

| 要求 | 最高接缝与可验证证据 | 必须失败或保留的负向用例 |
| --- | --- | --- |
| 单一持久布局与唯一 owner | installed CLI 的 prepare→受控 record→mock→verify→采用；同场景前后 inventory/字节数及路径归属比较，逐文件 hash 保留 | 新 final case 与 fixture 发生重排、重复整树 source/record snapshot、多连接复制 execution、误删空日志 |
| 来源完整与新旧导入 | 公开 Prepare/Open/Export 与 installed CLI 交叉；三类输入导入后删除外部输入、再次 export，比较原文件原字节 | source 只有 hash 缺文件、截断 project snapshot、legacy 自动迁移、旧 fixtureId 改写或旧 execution 进入本地调度 |
| 核心与晚出业务事实 | installed official record→公开 agent record，暂停父 driver 到 core 提交后再输出报告；前后 core inventory/hash 一致 | 父 driver 后失败、未触发、采集领取后失败、通知丢失；按唯一归属规则结算或保留未结算事实，不能回滚提交或补成功 |
| 原子边界与恢复 | installed CLI 对明确 journal 的故障点注入：core rename 前后、current rename 前后、staging rename 前后、索引写入失败 | writer 未退出时清理、两处副本/悬空 staging、旧 current 损坏、凭 latest 恢复；重复恢复/采用不得造第二份事实 |
| 路径与通知 | installed CLI result fd 完整行与落盘对象比较；公开 Go 运行时对象在新目录返回绝对路径 | fixture/package/material 基准混用、错 material/execution/run/connection、绝对/非规范/符号链接引用、未知/缺版本、坏 hash |
| 历史与 SDK 启动前读取 | 公开 Go StartMockAgent/ExecutionStart 与 installed CLI route 启动前 discovery；绑定文件、必要 Claude derived 原字节/双向 hash | template 缺完整启动环境、跨 execution 写污染、未知历史被绑定、源/派生篡改；不以通过代替 SDK session_cwd_mismatch 修复 |
| SDK 宿主串行责任（claim 前） | 公开 Go/CLI 消费者侧受控协调流程：暂停 A 于 route 读取后、agent claim 前，让 B 请求读取 route 并启动；B 在 A 的完整使用结束前不得进入 route/discovery/SDK 启动，暂停 A 的可写操作后核验其模板/已存在 overlay 与冻结 source 的 hash 不变；official driver 场景同样由外层调用方协调 | 两宿主在首次 claim 前同时使用同一 fixture；测试证明调用方协调区间，不宣称快照或 action 锁强制保护 SDK 启动 |
| SDK 宿主串行责任（claim 后旧快照） | A 已 claim 时给 B 先前取得的 route；原始读取及字段检查可以成功，但消费者协调流程必须阻止 B 用它启动 SDK，直到 A 全部宿主/peer 停止并恢复 route，B 才能重新取得当前 route；暂停 A 的可写操作后核验活动 overlay/source 未被 B 改写 | 单独以直接重复 agent claim 验收框架 busy，只证明 claim 时拒绝；不能据此宣称框架阻止 B 在 claim 前使用旧快照或访问共享模板；合法同 execution 的 main/probe 仍可并发 |
| 计划与运行输入 | installed official driver request + runtime 1；核对 consumer/official sourcePlanSHA256 与派生 planSHA256 | 把 source plan 与 material plan hash 混同、historyBindings 只写 raw source、持久域引用已删除 request/runtime/case.json 或秘密配置 |
| 选择闭包与兼容资格 | installed official verify 的显式采用入口；重新 prepare 采用包后只新增本地 execution，原闭包不变 | record/verify 相同内容被当同一事实、漏 connection/receipt、复用另次 passed、review/adoption 重复库存、standalone core 缺录制业务证据仍被采用 |

不使用整库精确文件数量作为门禁；比较同一受控场景的逻辑库存、必要副本和外部行为，
并把旧 data 的原字节检查单独保留。完整测试、race、driver/wheel 和构建要求仍按开发
规范执行；门禁失败不能通过修改 build identity、放松 VCS 校验或跳过原用例消除。

默认测试全程离线，不启动真实 Agent、不新增录制。真实录制、官方验证、SDK 仿真和
产品 E2E 分别报告，不能跨证据层抵扣。当前材料状态、缺口和官方环境限制分别由
[data/index.json](../data/index.json)、
[data/builtin/manifest.json](../data/builtin/manifest.json) 和
[driver 环境说明](../drivers/README.md#官方-sdk-环境)提供。
