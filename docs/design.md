# agent-sdk-testify 工具设计

`agent-sdk-testify` 是面向多语言 SDK 的独立测试工具。公开 CLI 入口为
`prepare`、`record`、`verify`、`mock` 和 `finish`；其中 `prepare`、`finish`
负责环境会话的生命周期，业务动作只有 `record`、`verify` 和 `mock`。工具维护
case、官方 driver、录制材料、固定索引与内置快照；消费方的业务测试编排和 SDK
仿真归消费方自己维护。

核心边界见下表；仓库路径、编码和测试规则见[开发规范](development.md)。

## 核心边界

| 命令 | 职责 | 执行方与对端 | 输出结论 |
| --- | --- | --- | --- |
| `prepare` | 按 source 创建隔离环境会话 | 工具 → 隔离根、workspace、HOME、config、tmp、历史和材料 | 会话、允许的业务动作和启动 descriptor |
| `record`（case source） | 按本仓库 case 录制真实 Agent 交互 | 官方 driver → 官方 SDK → 录制代理 → 真实 Agent | 业务执行、采集完整性、退出和清理结果 |
| `record`（plan source） | 由外部客户端驱动录制真实 Agent 交互 | 消费者客户端 → 录制代理 → 真实 Agent | 采集完整性、退出和清理结果 |
| `verify` | 用官方 SDK 验证已有录制 | 官方 driver → 官方 SDK → 严格回放引擎 | 官方业务断言、协议匹配、完整消费、EOF 和清理是否通过 |
| `mock` | 使用录制充当 Agent | 消费者自己的测试 → 被测 SDK → mock | 每个连接的协议匹配、完整消费、EOF、退出与清理结果 |
| `finish` | 结束环境会话 | 工具 → 会话、业务 action 和目标进程 | 业务证据、历史哈希、清理和最终状态 |

`simulate`、Go driver 和 SDK 覆盖门禁不属于本工具。SDK 或 MASS 通过进程接口消费
`mock`，工具不替消费者执行其业务断言。

| 模块 | 职责 |
| --- | --- |
| CLI 与生命周期 | 创建会话、绑定业务动作、管理进程、汇总证据和清理会话 |
| 官方 driver | 通过官方 SDK 执行一次 case，并给出业务结果 |
| 协议与回放 | stdio 采集、严格匹配、关联 ID 校验、完整消费、EOF 和回执 |
| 材料管理 | 资格校验、私有归档、采用、固定索引和内置快照 |
| mock | 用材料替代 Agent，提供查询和 prepared 会话动作 |

生产代码和默认测试不导入任何 SDK 实现。官方 SDK 只是 `record`、`verify` 的显式
环境依赖，不在运行时自动安装。

## Prepared session 生命周期

CLI 的规范使用顺序是：

```sh
agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
  --session-dir /private/session

agent-sdk-testify record --session-dir /private/session \
  --cmd /absolute/codex -- app-server --listen stdio://

agent-sdk-testify verify --session-dir /private/session

agent-sdk-testify mock --session-dir /private/session

agent-sdk-testify finish --session-dir /private/session
```

`--session-dir DIR` 在所有 prepared-session 命令中含义一致：`prepare` 创建会话
目录，`record`、`verify`、`mock` 和 `finish` 打开同一个会话目录。`session.json`
是工具拥有的内部 descriptor，调用方不直接提供或定位该文件。

会话目录是持久控制面和证据根，不是原生 Agent 的 home，也不是 `CODEX_HOME` 或
`CLAUDE_CONFIG_DIR`。隔离 `HOME`、config、workspace、tmp、历史和 `resourceRoot`
由 prepare 在工具拥有的临时根中创建，并通过 `session.json` 的 route 环境交给
executor；finish 清理这些临时资源，但保留会话目录。

`record`、`verify` 和 `mock` 都可以重复执行。一次 prepare 后的环境可以被多个
action execution 复用，例如 `record → verify → mock`、`record → mock`、连续多次
`mock`。同一会话的 action 串行执行；同一 execution 内的不同连接可以并发。上一个
action 完成后，
工具把本次 route 槽恢复为基线，下一个 action 才能开始。
`prepare` 和 `finish` 是独立 CLI 阶段，不是业务动作的内部实现细节。

execution 不是新的 CLI 子命令，而是 `record`、`verify` 或 `mock` 某一次实际执行
产生的持久记录。每次 action 调用生成一个 `executionId` 和独立目录；它用于隔离
workspace、回执、材料和失败证据。

`prepare` 必须给出来源之一：

- `--case CASE`：官方 case；可直接 `record`，有内置材料时也可直接 `mock`。
- `--plan FILE`：consumer plan；可直接 `record`，录制完成后可 `mock`。
- `--recording PATH`：官方或外部材料；可直接 `verify` 或 `mock`。

prepared session 的 `--case`、`--plan` 和 `--recording` 只在 `prepare` 选择且彼此
互斥；后续业务动作不重复 case 或 plan，而是从 `session.json` 和会话 execution 索引
读取不可变初始来源、允许动作、最新兼容录制和环境。managed mock 别名没有 prepared
session 时，可一次性选择 `--case` 或 `--recording`。

### 三个阶段

- **prepare**：校验 source 和 agent type，创建 `sessionId`、不可变环境基线、工具拥有
  的 `resourceRoot`、HOME、config、tmp、历史、材料和锁。它为当前 source 可达的
  route 预先物化环境和连接槽，但还没有执行任何业务动作。prepare 不启动客户端、
  SDK、driver 或真实 Agent，最后写入 `session.json`。
- **action**：每次调用 `record`、`verify` 或 `mock` 都创建一个新的 `executionId`，
  在 `executions/<executionId>/` 下写入不可变 `execution.json`，从环境基线创建本次
  execution 的可写 overlay，然后执行该链路或暴露连接。官方 executor 由工具启动；
  外部 executor 由消费者 SDK 启动 `connection`。同一 execution 的每个连接只能启动
  一次；连接全部结束后写回执、材料 manifest/report 和哈希，并把本 execution 标记为
  完成。
- **finish**：读取 `session.json` 和全部 execution descriptor。它逐 execution 核验
  action、source、target、严格匹配、完整消费、EOF、历史哈希和退出状态，写入每个
  execution 的最终核验结果，再生成会话级 `finish.json` 和 closed 标记，最后清理环境
  基线和临时根。没有任何 execution 的会话只记录 abandoned。工具不删除会话目录、
  业务输出或调用方目录。
  finish 可重试。

`finish` 就是正常 teardown：业务证据校验和资源清理在同一次收尾中完成，不提供另一套
并行的 `teardown` 成功语义。清理失败保留 `cleanup.json`，可重试；会话没有 action、
部分 action 失败或清理成功都不会把失败 execution 变成成功。

### 多次使用与产物传递

`session.json` 至少包含 `schemaVersion`、`sessionId`、`tool`、`agentType`、
`source`、`sourceSHA256`、`resourceRoot`、`workspace`、`home`、`config`、`tmp`、
`env`、`routes`、`finish` 和 `limits`。`routes` 描述当前 source 支持的 action、
executor、target、provenance、稳定环境和连接槽。`session.json` 创建后不因某个
execution 的产物而改写；可变状态只写入受锁保护的 append-only execution/artifact
索引。显式提供 `--history-seed` 时，case/plan 会话另在 `historySeed` 保存其规范化
来源路径。

`env` 只包含可持久化的隔离路径和非秘密运行配置。认证、代理凭据和原生配置派生的
私有变量只写入 `resourceRoot` 内的私有文件，record 启动时从当前进程或该文件合并；
变量名在 record route 的 `runtimeEnv` 中声明，值不进入 `session.json`、
`execution.json`、capture 或 finish 证据。

`session.json` 不是用户配置，也不是 Codex 的 `config.toml`、Claude 的
`settings.json` 或任何原生 session 文件。它是工具生成的内部只读会话合同，绑定
session 身份、工具和 source 身份、派生环境路径、允许 action 及 route 合同；
格式版本不支持时必须拒绝，调用方不得手工修改。

每次 action 的 `executions/<executionId>/execution.json` 至少包含 `schemaVersion`、
`executionId`、`sessionId`、`command`、`args`、`source`、`executor`、`target`、
`provenance`、`workspace`、`protocolCwd`、`env`、`connections`、`receipts`、
`artifactRoot` 和 `limits`。`timeout`、采集上限、验证采用参数等 action 选项在创建
该 execution 时写入，finish 不重新解释。环境基线只读；每个 execution 使用独立可写
overlay，所以一个 execution 的历史、fixture 或失败不会污染后续 action。

verify/mock connection 的材料路径和 plan/manifest/raw 哈希指向本次输入材料。record
的输入是原生交互；capture 发布后，execution connection 和 `artifact` 共同绑定输出
材料的路径及 plan/manifest/raw 哈希，不在启动前伪造未知的输入哈希。

`record` 完成后把 capture 登记为会话内不可变 artifact。后续 `verify` 和 `mock`
默认选择最新的兼容 capture：

- `record → verify`：verify 消费刚完成的 capture，不修改或消费掉它。
- `record → verify → mock`：mock 继续消费同一 capture；verify 只新增验证证据。
- `record → mock`：mock 直接消费 capture，不要求先 verify。
- plan source 的 `record → mock`：consumer material 可以进入 mock，但仍不能进入 verify。
- 没有 record artifact 时，recording source 直接为 verify/mock 提供材料；case source
  的 mock 使用内置材料，若该 case 没有内置材料则必须先生成 capture。

record 完成时，工具只把回放所需的允许原生 session/history 子树快照到 capture
artifact，不复制整个 Codex/Claude home 或配置目录，也不把认证、插件、个人指令和
无关会话写入材料。verify 和 mock 不启动真实 Agent，不生成新的可复用原生录制；
它们在 execution overlay 中产生的临时宿主状态不会自动提升为 capture。finish 删除
临时 `resourceRoot`，但保留会话目录中的 capture、execution 回执和最终证据。

`verify` 和 `mock` 可以显式提供 `--recording PATH` 覆盖默认的最新 artifact；路径
必须与会话 agent type 和协议格式兼容。多次 record 会新增 capture 并将它标记为最新，
旧 capture 仍保留并可按显式路径选择。

会话为外部 SDK 使用的 verify/mock 等在消费前已经具有稳定的 route 环境、历史副本和
连接命令。record 发布新 capture 时原子刷新下游 route 槽，然后才把 execution 标记为可用；
消费者在启动 SDK 宿主前读取该槽，因此 `record → mock` 不需要重新 prepare。

`source.kind` 取 `case`、`plan` 或 `recording`；`executor.kind` 取
`official-driver` 或 `external-sdk`；`target.kind` 取 `native-agent` 或
`replay`；`provenance` 取 `official`、`consumer`、`builtin` 或 `external`。

| 业务动作 | 有效 source | executor | target | 产物 |
| --- | --- | --- | --- | --- |
| `record` | 官方 case 或 consumer plan | 官方 driver 或消费者 SDK | 真实 Agent | official capture 或 consumer material |
| `verify` | 材料 | 官方 driver | 严格回放 | verification evidence |
| `mock` | 内置或外部材料 | 消费者 SDK | 严格回放 | mock execution evidence |

### 环境归属

prepare 负责所有 action 共用的隔离根、workspace 模板、HOME、config 目录框架、tmp、
所有权标记和锁，并为同一 source 可执行的全部 route 物化对应的环境。case 同时支持
录制和 mock 时，原生配置与回放历史放在相互独立的 route 目录；recording 的 verify 和
mock 共享只读材料身份但使用各自的运行 overlay。消费者只能使用会话返回的 route
环境，不能提供或覆盖路径。

source 校验、原生配置发现与筛选、历史派生、fixture 写入仍全部由工具完成。prepare
完成后，source、原始材料、plan、`session.json` 和环境基线不可改写；action
只写自己的 execution 目录、回执、artifact 和索引状态。

会话目录是持久输出，规范布局如下：

```text
<session>/
  session.json
  source/plan.json          # case 或 plan 的不可变来源副本
  source/history-seed/      # 可选，显式历史种子的不可变副本
  executions/
    index.json
    <executionId>/
      execution.json
      record/ | verify/ | mock/
      receipts/
      finish.json
  prepare.json             # prepare 失败诊断，如需要
  finish.json
  cleanup.json
```

临时 `resourceRoot` 位于系统临时目录并带所有权标记，由 finish 清理；会话目录和其中
的业务输出不会被 finish 删除。

会话目录是运行控制面和私有证据根，默认不进入 Git；示例使用仓库外的私有路径。
工具不自动执行 `git add` 或提交。只有显式 `verify --index` 的资格审核和原子采用可以
写入 `data/index.json` 及 `data/<agentType>/<story>/<case>/<attempt>/`；之后显式生成
内置快照时再写入 `data/builtin/`。consumer session、consumer material、原始失败尝试、
私有归档和临时 `resourceRoot` 都不进入仓库数据。

finished 会话不能再创建 execution。准备阶段失败、action 失败或 finish 失败都保留已有
核对证据；成功 execution 和失败 execution 在会话级 `finish.json` 中分别列出，不能互相抵消。

### Managed mock 别名

`mock` 继续支持不经过显式会话、直接作为 Agent executable 的单连接 managed mock
别名：

```sh
agent-sdk-testify mock --agent codex --case SDK-CASE-001 \
  --report /reports/direct.json
agent-sdk-testify mock --recording /captures/attempt \
  --report /reports/direct.json
```

该托管别名内部仍使用同一 prepare、action、回放和 finish 状态机，只省略持久会话。
它只适用于宿主已经拥有一致环境、单连接且不需要外部收尾的场景；`record` 和
`verify` 没有隐式会话别名。

### 不变量

- prepared-session 生命周期只在 prepare 选择 `--case`、`--plan` 或 `--recording`，
  prepare 后不可改写；managed mock 别名的一次性 `--case`/`--recording` 选择不属于
  第二个生命周期。
- prepare 不启动客户端、SDK、driver 或真实 Agent，也不执行业务断言。
- 一次会话可以包含多个 execution；每个 execution 使用唯一 `executionId` 和不可变
  `execution.json`。
- 每个 execution 从环境基线派生独立 overlay，结束后恢复 route 槽，不能修改基线或
  另一个 execution 的状态。
- 同一会话的 action 串行执行；同一 execution 的连接才可并发。
- 同一 execution 对每个声明连接最多启动一次；失败也保留 receipt、原始帧和 stderr。
- record artifact 发布是下游 verify/mock 可见的唯一交接方式；最新选择必须确定。
- finish 只在客户端、连接和 SDK 宿主全部关闭后完成证据核验，但清理始终尽力执行。
- finish 只删除工具拥有的临时根，不删除会话目录、原始材料或调用方目录。
- 官方和 consumer 的 provenance 不混用；consumer material 不进入 verify、采用
  或内置快照。
- 回放继续使用严格匹配，不因会话生命周期而增加通配、忽略字段或 UUID 生成。

### 共享与差异

`prepare` 和 `finish` 是 CLI 上的独立阶段，内部各自只有一份实现。prepare 共用会话
身份、隔离环境、所有权、锁、source 校验和失败诊断；差异仅在 case、plan、
recording 的路由与材料准备。finish 共用身份核验、逐 execution 回执、哈希、报告、
幂等清理和重试；差异仅在每个已执行 action 的证据要求。

`record`、`verify`、`mock` 只负责创建 execution、解析默认/显式 artifact，并通过
executor/target adapter 启动或暴露连接。三者不复制 prepare 或 finish 逻辑。

## 命令合同

### prepare

```sh
agent-sdk-testify prepare --agent codex --case SDK-CASE-008 \
  --session-dir /private/session
```

`prepare` 只创建一次可复用会话，不启动任何业务动作。`--session-dir` 必须是不
存在的持久目录；prepare 创建该目录，并在其中写入 `session.json`、case/plan 来源
副本和后续 `executions/` 状态，环境基线写入工具的临时 `resourceRoot`。source 还可用
`--plan` 或 `--recording` 提供，三者互斥。
agent type/case/plan/recording 的组合在 prepare 时完成校验；后续 action 不能换
source 或 agent type。

`--agent codex|claude` 在 source 能唯一确定时可省略：plan 从自身声明推断，
recording 从 manifest 推断，case 只有在只有一个可用 binding 时才自动选择；存在多个
binding 时必须显式给出 `--agent`。显式值与推断值不一致时 prepare 失败。

### record

```sh
agent-sdk-testify record --session-dir /private/session \
  --cmd /absolute/codex -- app-server --listen stdio://
```

record 从 prepared source 选择 case 或 consumer plan，创建新的 execution 和可写 overlay，
调用官方 SDK 或消费者 SDK 对接真实 Agent，保存原始双向帧、stderr、plan、来源
manifest、业务报告、采集和清理结果。产物登记为会话内新的 capture artifact。

真实 Agent 启动参数使用 `--cmd` 和 `--` 分隔：`--cmd` 必须是绝对 executable
路径；`--` 后的每一项表示一个 argv 元素，原样传入、不经过 shell。`--` 之前的
参数属于 record，`--` 之后即使是 `--listen` 等选项也只交给真实 Agent。

只有 `record` 可以连接真实 Agent，且必须由操作者显式执行。失败、未触发和不支持
的尝试同样保留原始证据。录制成功只代表本次采集达到其业务、采集与清理要求，仍需
`verify`；不自动执行资格判断或采用。

`prepare --plan` 的 record 由消费者 SDK 驱动。消费者从会话 route 槽取得
workspace、HOME、config、tmp、历史、连接命令和本次 execution descriptor，在启动自己的
SDK 宿主前应用这些值；不能提供或覆盖隔离路径或原生环境变量。客户端仍是消费者自己
的 SDK，不调用官方 driver。产物是 external/consumer material，不进入官方 case、
`verify`、资格审核、固定索引或内置快照。合同、plan 和材料布局见
[消费者录制](consumer-recording.md)。

### verify

```sh
agent-sdk-testify verify --session-dir /private/session --timeout 2m
```

verify 默认消费会话中最新且可验证的 capture；可用 `--recording PATH` 显式选择。
agent type/case 从材料身份确定，工具只选择对应的官方 driver。对端使用严格回放引擎，
不连接真实 Agent。每次 verify 创建独立 execution 和验证证据，不消费或改写 capture。
通过须同时满足：

- 原始文件、plan、manifest 和来源哈希有效。
- 官方 driver 的业务断言通过。
- 所有声明连接严格匹配并完整消费。
- 客户端 EOF、进程退出和工具自有资源清理成功。

验证不改写原始录制。验证证据与原件分开保存，并绑定录制、合同、工具和 driver 哈希，
同时记录实际安装的官方 SDK 包及版本。`--index` 可在验证成功后执行资格校验、私有
归档和原子采用；采用失败保留验证证据和旧索引/材料。

### mock

```sh
agent-sdk-testify mock --session-dir /private/session \
  --connection main
```

规范形态是在 prepared 会话中创建 mock execution。默认消费最新的兼容 capture；没有
capture 时，recording source 使用 prepared 材料，case source 使用内置材料。可用
`--recording PATH` 显式选择。每次 mock 创建独立 execution；`main` 和 `probe` 可以
并发，同一 execution 的同一连接不能重复启动。

多连接只用于 case 确实要求多个独立原生客户端生命周期的流程，不是会话或 Agent 的
通用多路复用。当前官方 `remote-close`（SDK-CASE-032）使用 `main` 和 `probe`：
`main` 完成创建、交互和主动断开；`probe` 是之后独立的第二个客户端，用同一远端
服务读取已保存结果，以证明第一个连接关闭没有终止服务。两条连接各有独立 raw、
receipt、进程生命周期和 EOF，不能合并成一条协议流。其他普通 case 只有 `main`；
consumer plan v1 也只支持 `main`。

单连接、无宿主环境要求时，直接替换 Agent executable 的 managed mock 别名仍可使用：

```sh
agent-sdk-testify mock --agent codex --case SDK-CASE-008 \
  --report /reports/mock-001.json
agent-sdk-testify mock --recording /captures/attempt \
  --report /reports/mock-001.json
```

需要父进程环境、初始文件、历史、多连接或多次复用时，使用 `prepare`、`mock` 和
`finish` 会话。默认启动模式的 stdout 只承载原生协议；查询与收尾输出结构化结果，
诊断写入 stderr 或报告文件。命令、描述符和错误语义见
[mock 使用指南](using-mock.md)。

### finish

```sh
agent-sdk-testify finish --session-dir /private/session
```

finish 不接受 action、source 或连接参数。它结束整个会话，汇总所有 execution 的业务
证据，执行历史哈希和资源清理，然后写入 `finish.json` 和 closed 标记。会话可以没有
execution；
这种会话按 abandoned 清理，不产生业务成功。finish 可重复调用。

## Case 与 agent type 配置

[场景目录](../data/scenarios)保存用户故事、case、输入、预期和 agent binding。
case 身份、输入顺序、required/optional、`live-selected-path`、`planned` 和
`unsupported` 都是权威数据；人工构造的验证输入不能替代真实使用流程。

官方 driver 接收一个请求并写入 `result.json`；请求与结果字段见
[driver 文档](../drivers/README.md#单请求协议)。运行器负责环境、进程、超时、取消
和协议回执，不解释 SDK 业务事件。

新增 case 同时更新场景、agent binding、验收规范、测试和固定索引要求；不能只
增加一条成功录制或降低现有 required 集合。全部用户目标和通过边界见
[共同验收](cases.md)。

## 环境与隔离

`prepare` 为会话创建独立环境基线，包括 workspace、HOME、config、tmp 和历史；
每次 action 从该基线创建自己的可写 overlay，只释放 case 或 consumer plan 声明的
文件。一个 execution 的副作用不会进入另一个 execution。Codex 使用 `CODEX_HOME`；
Claude 使用
`CLAUDE_CONFIG_DIR`。认证只来自工具选择的允许配置和当前进程中的白名单认证变量，
不复制个人 skills、插件、记忆、项目指令或无关会话。

`--case`、`--plan` 和 `--recording` 都不暴露配置目录输入；工具按 agent type 从标准
原生配置位置发现并筛选副本，外部只可修改 Agent executable 和启动参数。消费者必须
在启动自己的 SDK 宿主前应用当前 route 的环境、cwd、历史和连接命令；只设置录制或
mock 子进程的环境不能配置父进程。

record 完成后原子发布 capture 并刷新下游 route 槽；verify 和 mock 使用新的 execution
overlay 消费该 artifact。plan source 的 record 由消费者显式执行 `record --cmd ... --`
并应用 route 环境；mock 使用同一 route 的连接模板启动严格回放。两者共享环境和历史
身份，但 record 的启动参数不能被伪装成预先已知的回放连接。
record 在 execution 完成时快照隔离环境中的历史；finish 只重新核验快照和哈希，不读取消费者
个人目录。

Codex 的原生项目 cwd 保持录制身份，可写 fixture 使用新的 workspace。Claude 在
新目录键下派生历史副本，只替换必要的顶层 cwd/project 身份，保留 ID、未知字段及
数字字面量。`materialization.json` 记录来源与派生路径及双向哈希；派生副本不能
冒充原件。

## 录制、材料与发布

一次采集按以下结构保存：

```text
data/<agentType>/<story>/<case>/<attempt>/
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
- consumer material 只由同一会话的 mock 或显式 `mock --recording` 按路径消费，
  不写入 `data/index.json` 或 `data/builtin/`；官方采用必须重新走官方 case 录制、
  `verify` 和资格审核。

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
不支持的分支或协议明确失败，不搜索其他录制、不猜测 agent type、不启动真实 Agent。
consumer material 使用同一严格回放引擎；它不会因为来源是消费者而增加通配、
字段忽略或 UUID 自动生成规则。消费者必须提供可重复的客户端输入，或使用自己的
stateful peer。

## 生命周期与证据

prepare 为整个会话创建带所有权标记的临时根、环境基线和连接槽；每个 execution 使用独立
overlay，每个声明连接只能启动一次。外部 SDK executor 的消费者先关闭自己的
Session/Runtime 和宿主进程，再执行 `finish`；即使某个 execution 的业务断言失败也必须
收尾。

finish 逐个 execution 核验声明连接的身份、严格成功、完整消费、EOF、原生启动状态、正常
退出及历史哈希，然后只删除工具拥有的临时根。占用、清理或哈希失败保留在报告中，
可以重试；工具不按进程名杀进程，也不删除调用方目录。record、verify、mock 共享这些
状态和回执语义，命令差异只在 source、executor 和 target。

业务通过、协议通过、材料验证和发布资格是不同结论：

- `record` 只报告本次真实采集。
- plan source 的 record 只报告消费者客户端的采集完整性；业务断言由消费者报告。
- `verify` 报告官方业务断言与严格回放。
- `mock` 只报告协议、退出和清理；消费者报告其 SDK 业务结果。
- SDK 仿真和产品 E2E 由消费者单独执行和报告。

默认检查全程离线，不启动真实 Agent、不新增录制，也不把合成测试或局部通过升级为
完整覆盖。当前材料状态、缺口和官方环境限制分别由
[data/index.json](../data/index.json)、
[data/builtin/manifest.json](../data/builtin/manifest.json) 和
[driver 环境说明](../drivers/README.md#官方-sdk-环境)提供。
