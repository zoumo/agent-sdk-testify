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
| Material | 完整、不可变的录制材料包，包含 plan、manifest、raw、绑定历史和来源哈希。 |
| Current recording | fixture 当前选中的唯一 material；verify、mock 和 export 以它为输入，record 成功时替换它。 |
| Action execution | 一次 record 或 mock 产生的 execution，具有独立 descriptor、overlay、receipt 和失败证据。 |
| Action result | record 或 mock 返回的连接/execution 状态、材料及证据路径。 |
| Receipt | 连接或进程的持久结果，记录协议、退出、清理和诊断。 |
| Verification | 一次官方验证的事实与结论，绑定精确 material、driver 身份及实际参与的 mock execution；不拥有协议连接或 execution。 |
| Admission | 把具备资格的材料显式归档并写入默认 case 库的动作。 |
| Export | 把 current recording 的完整材料包复制到 fixture 外的只读导出动作。 |

Material 是回放输入，不必先经过 verify。Verified 是 material 上的一份证据，不是
material 的固有类型，也不会随着 current recording 切换而转移。

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
- `--recording PATH`：已有录制材料。工具把完整材料包复制进 fixture，并把副本设为
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
- 创建 fixture 目录及其 runtime 子目录、环境基线和连接槽。
- 将 case、plan 或已有录制材料完整复制到 fixture，并校验副本哈希。
- 固化不可变的 record source：保存 plan、协议 cwd、agent binding 和来源身份。
  case 的官方 driver 由 official 命令选择，plan 的 SDK 宿主由消费者启动；agent
  record 不根据 source 类型启动 driver。recording 使用材料内的录制来源。
- 将当前材料写为 current recording；没有材料时保持为空。
- 写入 schema 6 的只读 `fixture.json` 和可变状态索引，不启动客户端、SDK、driver
  或真实 Agent。工具身份使用 `executable`/`embedded` 判别式：executable 保存版本
  和二进制哈希，本次启动命令在运行时解析并核验身份；embedded 记录模块路径、
  版本/修订和 Go 版本，不伪造 command、launcher 或进程身份。

prepare 不修改外部材料。fixture 目录必须尚不存在；来源路径不能位于目标 fixture
内。prepare 失败不产生可用 fixture，失败诊断保存在 fixture 内或 prepare 的诊断
文件中。schema 5 及更旧 fixture 必须拒绝并重新 prepare，不能按 schema 6 猜测字段。

### 多个 action execution

prepare 成功后 fixture 可反复使用。agent record/mock 创建 execution；official record/verify
保存独立的编排事实，不创建协议 execution：

1. 通过前置检查后才领取 execution id。
2. 从不可变环境基线派生本次可写 overlay。
3. agent record 启动或连接真实 Agent，agent mock 提供回放连接；材料来自 fixture。
4. 完成连接、进程、收尾和 receipt 持久化。
5. 原子更新 execution index；record 成功时额外原子切换 current recording。

同一 fixture 的 record/mock execution 串行执行；同一 execution 内声明的不同
连接可以并发。official 编排期间，driver 按普通合同启动 agent record 或 agent mock；
不预分配协议 execution，不增加跳过 execution 的官方专用模式。

库 `StartMockAgent` 在领取新 execution 或按显式 execution id 加入、claim 本次
connection 并写入 running result 后才返回 `MockRun.Start`；协议 I/O 不早于该快照。
新 execution 的后续声明连接必须显式加入同一 execution，不通过扫描最新目录或隐式
猜测归属。`MockRun.Wait` 只在 replay 与 finalize goroutine 结束后返回；多连接
execution 的最终事实由 `Fixture.ExecutionReport` 或 execution index 给出。

fixture 同时只允许一个 official 编排，并登记其运行状态。official verify 固定 current
material，期间拒绝 agent record、official record 和另一次 official verify，允许
agent mock。official record 允许其 driver 调用 agent record；不允许另一次 official
编排交错。官方录制的 execution 归属通过正常结果接口明确关联，不能靠最新目录推测。

official 编排登记与 execution 创建按同一 fixture 状态协调；official 命令等待 driver 时
不持有 agent 命令需要的 execution 锁。编排结束前等待 driver 及其启动的 agent
进程退出，保存事实后解除活动登记。中断遗留的执行和活动登记保留为未完成事实，
不能被后续成功结果覆盖；调用方负责等待自己启动的全部进程退出。

agent 命令与 official 命令可以重复调用，不要求成对出现，也不要求上一次验证成功。

### fixture 目录所有权

调用方拥有整个 fixture 目录。prepare 把运行环境放在 `<fixture>/runtime`，
运行时的 `resourceRoot` 指向该目录；工具不创建 fixture 外的长期运行根。

各 action 在返回前完成自身的协议、材料和资源释放检查，保存成功或失败结果。
mock 在结束时校验本次 execution 绑定的源历史和派生历史；运行中新建的非绑定文件
不成为回放输入。工具不在 fixture 结束时重新审计全部历史 execution。

调用方先关闭 SDK 宿主、等待相关进程全部退出，再按需删除 runtime 或整个 fixture。
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
| `export` | 仅 fixture 中的 current recording | fixture 外的完整材料包 | 否 |

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
  runtime/                  # 可丢弃的环境基线、route、overlay 和私有运行配置
```

`fixture.json` 只保存 schema、fixture 身份、工具身份、agent type、source 身份、
环境基线和连接合同，不在 action 执行期间改写。current recording、execution index
和 official 编排活动登记保存在受锁保护的可变文件中。

环境基线和每个 execution 的独立 workspace、HOME、config、tmp、历史 overlay
均位于 runtime。materials、executions、official-records 和 verifications 保存持久
材料与证据。目录释放时序见[fixture 目录所有权](#fixture-目录所有权)。

record 成功时，工具先把完整材料写入新的不可变 material 目录并校验全部哈希，再
原子更新 `current.json`。任何一步失败都不能把半成品设为 current。旧 material
和旧 verification 保留，但不再是 current，也不会被后续动作自动选择。

### 框架路径与录制身份

框架持久化身份、哈希和根目录内的相对引用；执行时从调用方提供的 fixture 根目录
计算实际绝对路径。材料的身份不包含它在某台机器上的存放位置。
原始协议、原生历史、日志、用户提供的 plan 和参数保持原样，不执行路径字符串替换。

文件引用只有两种基准：fixture 控制面中的引用相对 fixture 根目录，材料内部的引用
相对该材料根目录。固定布局由 ID 推导，不重复保存路径；其余文件引用使用规范的
`/` 分隔相对路径。读取前拒绝绝对引用、`..`、非规范路径和逃逸根目录的符号链接。
不引入通用 locator、路径占位符或对任意 JSON 字符串的替换机制。

| 字段或内容 | 持久化规则 | 运行时规则 |
| --- | --- | --- |
| material 的 `path`，connection/result 中的 material 路径 | 新材料不保存；控制面保存 material ID 和内容哈希 | 从当前 fixture 的 `materials/<materialId>` 定位 |
| fixture 的 `resourceRoot/workspace/home/config/tmp`，route 的 workspace | 固定布局不重复保存 | 根据当前 fixture 和 action 计算 |
| execution 的 `workspace/home/config/tmp/artifactRoot` | 固定布局不重复保存 | 根据当前 fixture、execution ID 和 action 计算 |
| source、history seed 和非固定布局文件 | 保存 fixture 内相对引用及来源哈希 | 只访问 prepare 已复制并验证的副本 |
| receipt、process receipt、result 和 descriptor 引用 | 保存 fixture 根相对路径；文件哈希表使用同一基准 | 根据本次 fixture 根目录读取 |
| launcher、框架命令中的 fixture 参数和目录环境变量 | 不保存可推导的本机位置 | 在启动前构造；工具身份仍须匹配 |
| `project/historyProject/_fixtureProject`、`native.cwd` 和原始参数 | 保留解释录制所需的身份与事实 | 不作为旧目录的文件读取入口，不替换为当前 workspace |

`protocolCwd` 是协议身份，不统一按 workspace 推导。record 和各 provider 的 mock
仍遵循各自合同；尤其 Codex mock 使用材料中的原始协议项目身份。`_fixtureProject`
所代表的录制工作目录继续用于解释已有历史和工具路径，不因它由框架生成而删除。
这些事实即使是绝对路径，也不要求对应目录存在。

新材料元数据不保存本机 `path`。已有材料中的该字段保留原字节，只作为旧格式事实，
不用于查找文件；加载器显式接收当前材料根目录。导入不为更新位置而重写旧
`material.json`、plan、manifest、历史或证据。fixture 的新索引与材料文件分开保存；
没有元数据的受支持旧材料，其导入身份保存在 fixture 索引中，不向旧包补写文件。

### 材料计划与运行配置

官方材料 plan 保存场景输入、实际使用的协议身份、原生参数、CLI 版本与哈希、
历史 seed 的 session 身份、文件绑定及来源哈希。它不缩减为原始 source plan 的副本：
录制准备阶段产生且回放或资格检查需要的事实必须进入材料闭包。
用户提供的 source plan 保持原字节并以哈希绑定。

`root/home/config/raw`、`_seedRoot`、当前 driver workspace、结果文件位置和连接命令
属于本次运行配置，不进入新材料 plan。driver 分别接收材料 plan 和运行配置：
读取原始协议身份使用前者，访问本次文件和启动进程使用后者。材料中的
`_fixtureProject` 不再被当前 workspace 覆盖；`_recordedWorkspace` 不作为另一份
持久身份重复保存。已有材料中的旧运行字段只按旧格式读取，不能用于定位当前资源。

prepare 完整复制并校验所需 seed 来源闭包和项目快照；后续录制的来源一致性检查
对这份冻结副本执行。新 seed 不保存外部 `sourceRoot`，使用 fixture 内的明确引用。
来源 plan/manifest、历史文件及项目快照的哈希检查不能因去掉旧目录而省略，
也不能只保存哈希而漏掉需要复验的文件。已封存 seed 中的原项目身份保持不变。

执行流程如下：

1. prepare 导入并验证来源，保存不可变副本、身份和相对引用。
2. record/mock 创建 execution overlay，生成本次绝对 workspace、环境和启动命令。
3. record 原样采集协议和历史，分别保存材料 plan 与执行证据，完成闭包后发布 current。
4. mock/official verify 从 current 获取内容，从本次 execution 获取运行位置；
   原始协议帧及严格匹配规则不变。
5. export 按材料原字节复制；新目录中的 prepare 建立新的控制面索引，不改写导入材料。

### 路径相关证据

录制隔离和当前验证隔离是两份不同执行的事实。录制时在真实执行环境中检查目录归属、
有效环境、启动参数和进程身份；新证据以 execution 身份、相对引用及哈希绑定结果。
归档后的资格检查验证这组封存证据及其关联，不用当前 verify 的 overlay 证明过去
录制正确。当前 verify 的隔离由它实际调用的 mock execution 独立证明。
已观察到的绝对 cwd、argv 可以保留为采集事实，但不能作为重开旧目录的依据。

旧录制证据按其原版本校验身份、哈希和路径关系，不访问旧目录，也不因含绝对路径而
自动拒绝。缺失证据、未知版本、关系冲突或哈希错误仍然失败；不能通过替换为当前
目录、只比较规范化哈希或忽略字段来接受材料。

CLI 结果通知与落盘 result 使用同一对象、同一相对引用和同一证据版本。
消费者根据已知 fixture 根目录解析，不能只把通知转换为绝对路径。
公开 Go 运行时对象中的路径字段继续返回当前解析后的绝对值，字段含义不变；
这些对象不作为原始持久证据重新序列化或参与原字节哈希比较。

可移植性通过 export 后在新目录重新 prepare 验收。直接移动整个已准备 fixture
并继续运行不属于此合同；route 符号链接、launcher 和活动执行不因此获得搬迁保证。
原始回放 `init.cwd` 不变，SDK 对旧 cwd 与当前物理目录的身份校验不在此设计中改写。

## schema 版本与兼容

`fixture.json`、`execution.json`、execution/material index 和 `current.json`
是同一个 fixture 控制面合同，共用一个 `schemaVersion`。这些文件在 prepare、
record、mock 和切换 current 时一起写入并相互引用，分别版本会产生无法判定的混合
状态。material/manifest 保留自身格式版本；进程 receipt、官方编排和 verification
中的 `schemaVersion` 属于各自证据合同，不与 fixture 控制面版本比较。

控制面使用 schema 6。新 `material.json` 使用 `formatVersion: 2`，不含 `path`；
无该字段的受支持旧材料按既有格式只读校验，不自动改写或接受未知版本。
结果通知与持久 result 使用 schema 2；新的 isolation、官方编排和 verification
证据在改变字段语义时各自使用 schema 2。原生录制格式及既有 raw/manifest 不因
这些元数据变化而升版。driver 的运行配置合同与材料 plan 分开版本化；
不识别该运行配置合同的 driver 在启动前失败。

持久化语义发生不兼容变化时才升版。以下任一变化必须升版：

- 删除或重命名字段，改变类型、单位、编码、必填性或缺失时的默认语义。
- 改变现有字段、身份、哈希、路径、状态或文件间关联的含义。
- 新增字段后旧版 reader 不能忽略该字段，或新版 reader 无法继续读取旧数据。
- 改变严格回放、failure class、证据归属等会影响既有持久化结论解释的规则。

仅修改注释、错误文案或内部实现，或者新增旧版 reader 可以忽略、缺失时有明确默认值
的可选字段，不升版。版本号单调递增，每次不兼容变更只加一，不复用已发布的值。
一次连贯的破坏性变更只升一次；不能为尚未发布的多个字段分批升版，也不能保留中间
格式。版本号、兼容判断、相关规格和拒绝旧版的测试必须在同一个变更中提交。

读取持久化合同前先校验版本。fixture 控制面只接受当前 `schemaVersion`；更旧和更新
的版本都在执行前失败，不按当前字段猜测、不迁移、不双读，也不接受混合版本目录。
fixture 由调用方拥有且可重新 prepare，因此控制面的兼容窗口为零：升级后重新
prepare；需要保留的材料先用旧工具 export，再用 `prepare --recording` 导入新
fixture。material/manifest 的格式变化按自身合同处理，不因控制面升版而改写。
`schemaVersion` 不是工具版本、协议版本或 SDK 版本，不能互相替代。

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
   连接、receipt 和清理，verify 不预分配这些对象，也不覆盖 mock 的材料选择。
4. 保存 driver 的业务报告、实际 SDK 身份、退出状态和诊断，核对本次实际调用的
   mock execution、连接 receipt 和进程退出事实，写入 verification。

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

export 把 current recording 的完整材料包复制到 fixture 外：

- 包含 material identity、plan、manifest、raw、绑定历史和来源哈希。
- 不包含整个 fixture、其他材料、全部 execution、receipt、verification 或 runtime。
- 不产生 verified、admission eligibility 或 admitted 结论。
- 不修改 current recording、execution index 或 fixture 状态。

目标目录必须位于 fixture 外且尚不存在。工具先写入同父目录下的临时目录，校验副本
哈希，再原子 rename 为目标目录。export 可以重复执行；不同目标互不覆盖。导出包
可以作为下一次 `prepare --recording` 的输入。

只要 fixture 中的材料仍存在且哈希有效，就可以重复 export。

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

只有同时满足以下条件的 material 才具备默认 case 库的入仓资格：

- 身份是精确的官方 case 和 agent binding。
- provenance 为 official。
- 官方录制业务事实完整、通过并与精确 material 关联；已有材料内的原生业务证据
  保留原字节，新增编排记录通过身份和哈希绑定，不以缺少事实推断成功。
- 对该精确 material 的官方 verify 通过。
- 资格检查确认来源、隔离、业务结果、材料闭包和哈希均有效。

资格判断和采用都不能由 record、mock 或 export 隐式触发。采用是显式、
原子、可重试的仓库写入；写入前保留旧索引，写入失败不删除已验证材料。

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
- 库 `StartMockAgent` 必须使用与 CLI mock 相同的 execution、严格回放和 finalize；
  进程内连接不得伪造 executable、PID、launcher 或退出码。
- fixture、execution 和 index 使用 schema 6；embedded 工具身份不得把 module path
  当作 command，schema 5 及更旧 fixture 明确拒绝。
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
record execution/material；没有采集 execution 时明确记录未启动。driver 在采集
发布后失败，编排结果为失败，但已发布材料及其 current 指针不因业务失败回滚。
等待子进程退出后才能结束编排登记；未清理完成不能报告成功。

record 领取后失败时，current recording 不变；verify 失败时 current recording 不变；
mock 失败时 current recording 不变。任何失败都不能删掉旧材料或旧验证证据。

工具按以下顺序持久化：先写原始证据和 receipt，再写 execution 结论，最后更新
execution index 和 current pointer。verification 在 driver 和 mock 的事实落盘后
写入自身结论，之后才可显式采用。索引更新失败时，未索引的 execution 或 material
保持可诊断状态，不能伪装成成功。

凭证、认证变量和原生私有配置只写入 fixture/runtime 中的私有运行文件，不进入
fixture.json、material、execution、官方编排记录、verification 或 export 包。
远程 token 文件可以位于 runtime 或 fixture 外，不能位于 fixture 的持久证据区。

## 环境与隔离

prepare 为 fixture 创建独立环境基线，包括 workspace、HOME、config、tmp、历史种子
和连接槽。每个 record/mock execution 从基线派生可写 overlay，一个 execution 的副作用不能进入
另一个 execution。Codex 使用 `CODEX_HOME`，Claude 使用 `CLAUDE_CONFIG_DIR`。

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

record 完成时只快照回放所需的原生 session/history 子树，并记录来源、派生路径和
哈希。官方验证和消费者在 mock execution overlay 中产生的宿主状态不会自动提升为新的
material。删除 runtime 不影响材料导出；删除整个 fixture 前需要先导出待保留材料。

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
- export 后删除原 fixture 和外部 seed，在新目录 prepare 后仍可 mock、适用的
  official verify 和再次 export；不以直接移动 fixture 替代这项验收。
- 导入、验证和导出保持旧材料全部原文件的字节及哈希，不为修正路径重写元数据。
- 新控制面及材料中的定位字段不保存本机绝对目录；原始录制身份和采集事实单独校验，
  不按任意字符串中是否出现绝对路径判断材料合格。
- CLI 通知与落盘 result 完全一致；Go 运行时路径正确解析，相对引用越界和错误
  execution/material 关联必须失败。
- 录制隔离证据和当前验证隔离分别验收；丢失原录制证据不能由当前正确的 overlay 抵扣。
- record 成功原子替换 current；record 失败保留 execution 且 current 不变。
- verify 只作用于 current，并在新 record 后不把旧 verification 转给新材料。
- 没有 verified 的 current material 可以正常 mock。
- verify 不创建 execution；driver 必须调用公开 mock，由 mock 管理 execution。
- verification 精确引用本次实际启动的 mock execution，不能采用其他调用的成功证据。
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
- 多连接通过显式 execution id 加入；`ExecutionReport` 覆盖全部声明连接，进程内
  连接没有伪造的 PID、executable 或退出码。
- Codex 使用 `ProtocolCwd` 作为逻辑 transport cwd，Claude 使用 `Workspace`；
  两者使用 `ExecutionStart.Env` 替换环境，不修改全局环境。
- schema 5 及更旧 fixture 被拒绝，embedded fixture 不含 module-path-as-command；executable
  fixture 的进程 mock 路径继续通过 CLI descriptor 验收。
- driver 启动失败、提前退出、验证取消和 mock 缺失时保留验证事实；未完成的验证
  不能产生成功结论或允许清理仍在使用的资源。
- 旧顶层 record/mock/verify 和整个 internal-protocol 入口被拒绝。
- official record → driver → agent record 与 official verify → driver → agent mock
  都通过真实 installed CLI 接口；agent record 绝不再次启动 driver。
- driver 最终业务报告与 material 分开保存；采集成功后业务失败不篡改 material。
- agent record 的远程目标保留认证、双向采集、EOF、取消和连接关闭证据，不终止
  调用方已有服务，也不启动本地 Agent。
- mock 不响应 Agent CLI 版本或元命令。
- export 可重复、不改变 fixture、不产生入仓结论，导出包可再次 prepare。
- runtime 全部位于 fixture 内；调用方删除 fixture 后没有遗留运行目录。
- 删除 runtime 后仍能 export 材料；export 包不包含 runtime 或私有配置。
- Claude 源历史与派生历史在 mock 返回前校验，失败写入该 action 结果。
- 只有精确官方 case material 的 verify 通过才具备入仓资格；consumer material 和
  未验证材料不能进入默认 case 库。

默认测试全程离线，不启动真实 Agent、不新增录制。真实录制、官方验证、SDK 仿真和
产品 E2E 分别报告，不能跨证据层抵扣。当前材料状态、缺口和官方环境限制分别由
[data/index.json](../data/index.json)、
[data/builtin/manifest.json](../data/builtin/manifest.json) 和
[driver 环境说明](../drivers/README.md#官方-sdk-环境)提供。
