# 消费者录制

外部消费者可以使用 `prepare --plan` 创建 fixture，再由 record 把自身 SDK 或运行时发往
真实 Agent 的原生交互保存为 consumer material，并在同一 fixture 中直接用 mock 严格
回放。该路径不经过官方 driver，不登记官方 case，也不进入验证、采用或内置快照。

consumer 录制采用 `prepare → agent record → agent mock` 生命周期。一次
prepare 可以承载多个 record/mock execution；环境准备和材料由工具完整负责，
消费者只提供 plan、真实 Agent 启动命令/参数，并在自己的 SDK 宿主演用当前 route 的
环境。本文只描述 plan source；生命周期、source/executor/target 和 descriptor 合同见
[工具设计](design.md#fixture-生命周期)。

## 边界

- 工具负责隔离根、workspace、HOME、配置发现与筛选、tmp、历史种子、材料写入和执行内资源释放。
- 消费者负责 plan、真实 Agent executable 和启动参数、SDK 宿主、业务断言和 fixture 目录删除。
- 消费者不能提供或覆盖隔离根、workspace、HOME、config、tmp 或原生环境变量。
- 工具不启动消费者的 SDK 宿主，也不生成客户端请求；客户端始终是消费者自己的 SDK。
- 材料是 external/consumer 材料，只能通过 fixture 的 current 消费，不参与官方资格、采用和发布。
- consumer material 不能证明官方 SDK 兼容、业务断言通过、工具副作用发生或覆盖完整。
- v1 只支持一条 `main` 连接；`probe` 和其他多连接流程不在本版本范围内。

## 生命周期

### prepare

```sh
agent-sdk-testify prepare --agent codex \
  --plan /absolute/consumer-plan.json \
  --fixture-dir /absolute/fixture \
  [--history-seed /absolute/seed] \
  [--protocol-bytes N] [--stderr-bytes N] \
  [--max-frames N] [--max-frame-bytes N]
```

`prepare` 不启动真实 Agent。它创建 fixture 目录及其中的 runtime 运行目录，准备：

- `resourceRoot`、`workspace`、`home`、`config`、`tmp`；
- Codex 的 `CODEX_HOME` 或 Claude 的 `CLAUDE_CONFIG_DIR`；
- 从 agent type 标准原生配置位置发现并筛选的原生配置和认证副本；
- plan 的 `model` 配置和 `scenario.files`；
- 可选 `--history-seed` 历史种子；
- fixture 目录内的 schema 7 `fixture.json`、record/mock route 的环境槽和连接模板；
- 唯一的 `source/history-seed/` 冻结副本和 `packages/` recording package 根。

`--plan` 来源不接受配置目录参数。record 的 `--cmd` 必须是绝对路径；`--` 后逐项
提供 Agent 启动参数，不经过 shell，仍须通过回放白名单校验。
CLI 和原生参数属于本次 record execution，不改变已准备的 source、workspace、HOME、config
或历史。

### 配置发现

配置来源、允许文件、认证变量和隔离路径由[工具设计](design.md#环境与隔离)统一维护。
消费者不能通过 flag 覆盖这些路径，只使用 Go prepare/Open 返回的运行时对象，或 CLI 的 runtime route 快照；认证值不进入
`fixture.json`，调用进程只需保留 record route `runtimeEnv` 列出的变量。
持久定位字段与运行时绝对路径的区别见[框架路径与录制身份](design.md#框架路径与录制身份)；
prepare 返回的运行信息不能作为 `fixture.json` 的字段布局假设。

### agent record

```sh
agent-sdk-testify agent record --fixture-dir /absolute/fixture \
  [--timeout DURATION] \
  --cmd /absolute/codex -- app-server --listen stdio://
```

消费者先通过[SDK 宿主的运行时入口](design.md#sdk-宿主的运行时入口)
取得当前 record route，应用其 `env`，把 SDK 宿主 cwd 设为 `workspace`，然后执行
下文的 `agent record` 命令；Agent executable 和原生参数属于这次显式调用。record route 的
`connection.command`/`args` 是稳定调用前缀，其 `args` 已包含 `--cmd`，调用方在其后
追加 `/absolute/agent -- ARG...`。route 的 `runtimeEnv` 只列出来自调用进程或私有
运行环境的变量名，不包含值。record 从环境基线创建独立 execution overlay，在已准备
好的环境中启动真实 Agent，转发 stdio，并写入原始帧和传输报告。每个连接只能启动
一次；失败也保留原始证据。

record 成功后发布不可变 material 并原子替换 current recording；随后可以直接在同一
fixture 执行 `agent mock`，不需要重新 prepare。提交前失败保留 execution，current 不变；提交后交付失败保留提交事实，见
[封存、发布与恢复](design.md#封存发布与恢复)。record source
保持 prepare 固化的 plan 和 executor，不从 current 推导。consumer material 没有适用
官方 verifier，verify 在启动 driver 前失败。

record 通过前置检查时即领取 materialId（不是成功后才分配），并创建 `packages/<materialId>/`。
材料核心位于 package 的 `material/`，execution 直接位于同一 package 的
`executions/<executionId>/`。提交前失败 attempt 可以保留部分 package 和 execution，
但不切换 current；提交后的交付失败遵循工具设计的发布合同。多次成功 record 产生独立 package。

### agent mock

```sh
agent-sdk-testify agent mock --fixture-dir /absolute/fixture \
  --connection main
```

mock 只消费 fixture 中的 current recording，使用新的 execution overlay 和预先刷新的回放
环境。消费者按 mock route 应用 `env`、cwd 和 `connection`，执行结果由 mock 自身
写入 current package 的 `executions/<executionId>/`。consumer mock 不创建官方
`verify/` 证据。

### 释放 fixture

调用方关闭 SDK、等待相关进程全部退出后，直接删除 fixture 目录。需要保留 current
material 时先 export。运行环境和私有配置都位于 fixture/runtime；所有权与释放时序见
[工具设计](design.md#fixture-目录所有权)。

## 多次使用

- record 成功后，mock 使用新的 current；可在同一 fixture 直接执行。
- 多次 record 产生多个独立 recording package，旧 material 和旧执行证据保留；
  action 不接受材料覆盖。
- 需要在另一 fixture 使用材料时，先 export，再 prepare --recording；导出不提升可信等级。
- 每次 record 或 mock 使用独立 execution overlay，前一次 SDK 的历史和写入不会污染后一次。
- 同一 fixture 的 SDK 宿主使用区间按[运行时入口](design.md#sdk-宿主的运行时入口)
  由调用方协调；不能只等待上一个 execution 完成就复用 route。
- consumer capture 不进入 verify；需要官方验证时必须重新走官方 case 录制。

环境职责和认证边界见[工具设计](design.md#环境与隔离)。本路径的差异只有：消费者提供
plan、真实 Agent executable/args、自己的 SDK 宿主和业务断言。

## Consumer plan

`--plan` 输入是消费者提供的 source plan，version 1 使用以下字段。其原字节保存为
material/source-plan.json；material/plan.json 是唯一派生材料 plan。两者的哈希关系与
historyBindings 由[材料计划与运行配置](design.md#材料计划与运行配置)定义。

| 字段 | 要求 | 用途 |
| --- | --- | --- |
| `version` | 必须为 `1` | plan 格式版本 |
| `agentType` | `codex` 或 `claude` | 必须与 `--agent` 或自动推断值一致 |
| `caseId` | 非空字符串 | 消费者自定义身份；不是官方 catalog case |
| `driverProfile` | 必须为 `consumer` | 禁止官方 remote-close 和 probe 流程 |
| `project` | 绝对路径 | 原始协议 cwd；不是回放时的新 workspace |
| `model` | 可选字符串 | 写入隔离环境的模型名 |
| `cliVersion` | 可选字符串 | 来源记录中的原生 CLI 版本；mock 不模拟版本查询 |
| `scenario.files` | 可选对象 | 相对路径到文本内容的可写 fixture |
| `scenario.parameters` | 可选对象 | 消费者自定义参数；工具不解释 |

最小示例：

```json
{
  "version": 1,
  "agentType": "codex",
  "caseId": "consumer-two-turns",
  "driverProfile": "consumer",
  "project": "/absolute/original-project",
  "model": "model-name",
  "cliVersion": "native-cli-version",
  "scenario": {"files": {"fixture.txt": "fixture content\n"}}
}
```

未知字段必须保留。`project` 只表示协议项目身份；工具创建新的 workspace，消费者不能
把回放或录制 workspace 写进 plan 冒充原始 cwd。

`scenario.files` 只支持文本内容。二进制 fixture、认证文件和个人配置不属于 v1
consumer plan；需要历史的消费者使用 `--history-seed`。

## Fixture 与 execution descriptor

`fixture.json` 和 `execution.json` 的字段合同见
[工具设计](design.md#fixture-生命周期)。plan source 的 `protocolCwd` 保持
plan 声明的原始项目身份；消费者仍使用 execution 返回的 overlay `workspace`。

每次 record 或 mock 另在
`packages/<materialId>/executions/<executionId>/execution.json` 写入本次
execution 的只读合同，字段见
[工具设计](design.md#fixture-生命周期)。fixture control 的包选择使用 fixture 根
上下文并由 canonical material ID 推导固定 `packages/<materialId>/`；execution、
receipt、result 和 CLI 通知使用 package 根相对引用；material 内部引用使用 material
根相对引用。消费者不需要也不允许重写 source、control 或 package；CLI 宿主读取的 route
快照只在 runtime 内，不能把它当作持久 descriptor。
消费者不得 source、eval 或改写 `fixture.json` 或 `execution.json`。当前 route 的
`env` 必须应用到自己的 SDK 宿主，不能只设置录制或 mock 子进程。execution 创建后
不得重新生成或覆盖。

完成结算后删除 runtime，运行路径与 route 快照失效，material 仍可 export；删除整个 fixture 会同时删除
package、材料和执行证据。

mock route 的 `connection.launcher` 用于只接受 executable 的 SDK；launcher 把受支持
的追加参数转发给回放连接，并拒绝重复或白名单外的参数。record 使用上文的显式
`agent record --cmd ... --` 调用，不把 route 前缀当作完整 executable。

## 原生启动参数

v1 复用 `agent mock` 的原生启动参数白名单，避免录出一份无法回放的材料：

- Codex：`app-server`、`--listen stdio://`。
- Claude：`--output-format stream-json`、`--input-format stream-json`、
  `--permission-prompt-tool stdio`，可选 `--verbose`、
  `--include-partial-messages`、`--model=...`、`--resume=...`、`--await-claim`。

重复参数、未知参数和空格形式的 `--model VALUE`/`--resume VALUE` 在 record 启动前失败。
需要其他原生启动参数时，先扩展回放 adapter 和白名单；不能只放宽录制侧。

## 材料布局

record 的尝试与失败证据保存在 execution；成功材料发布到独立 material 目录：

```text
<fixture>/packages/<materialId>/
  material/
    material.json
    source-plan.json
    plan.json
    manifest.json
    report.json
    raw/
      raw.jsonl
      manifest.json
      transport.json
      stderr.raw
    history/
      seed/...
      runtime/...
  executions/<executionId>/
    execution.json
    result.json
    materialization.json
    history/derived/...
    isolation.json
    prepared-plan.json
    receipts/...
```

- `packages/<materialId>/material/` 是封闭 material 根；package 只以 canonical
  material id 命名，`current.json` 选择当前 package。
- `material/` 在成功 record 后不可改写；失败 attempt 的部分 package 和
  `executions/<executionId>/` 保留本次尝试与诊断。
- `raw/raw.jsonl` 使用 v1 stdio 帧格式，保留 ordinal、direction、channel、epoch、
  elapsedMs 和原始 wire JSON。
- core 的 manifest、plan/source plan、history 绑定、capture report 和完整文件库存统一遵循
  [文件 owner 与校验闭包](design.md#文件-owner-与校验闭包)；不维护第二套 consumer 库存合同。
- `raw/manifest.json`、`raw/transport.json` 与 `raw/stderr.raw` 保留原生采集格式和原字节。
- 不生成 `adoption.json`、`review.json`、`qualification.json` 或
  `official-replay/`。

## 状态与历史

`--history-seed` 是可选的完整历史种子，由工具校验后复制到隔离环境。record 完成时只快照
隔离环境中回放所需的原生历史，并发布为 material 内的规范布局：

```text
material/history/
  seed/...       # prepare 冻结 seed 在 material 生命周期内的副本
  runtime/...    # record 完成时的原生 projects/sessions 等历史
```

fixture source 与 material 的完整闭包、history/seed 与 history/runtime 的唯一副本、
historyBindings 及 Claude 派生规则统一遵循
[材料计划与运行配置](design.md#材料计划与运行配置)。consumer format 3 同样使用
source-plan 原件与派生 material plan，不再声称 planSHA256 同时绑定两者。

回放时，mock route 按绑定从 `material/history/` 把文件 materialize 到本次完整
可写 execution overlay；mock 返回前校验已绑定历史文件未被改写。SDK 在回放期间
可以创建新的会话或历史文件，但不能修改已绑定的种子文件。没有历史种子的录制从
空状态开始，只覆盖本次协议交互。

## 协议执行结果

`agent record` 和 `agent mock` 都接受可选的 `--result-fd N`（N ≥ 3）。
调用方为该次进程传入独立的可写文件或管道描述符；协议 stdout 不包含结果通知。
例如，在已应用 route 环境的宿主中保存一次调用的结果：

```sh
agent-sdk-testify agent record --fixture-dir /absolute/fixture \
  --result-fd 3 --cmd /absolute/agent -- app-server --listen stdio:// \
  3>/absolute/record-result.jsonl
```

领取 execution 后，命令先写入一条 `status: running` JSONL 通知，结束时再写入
`passed` 或 `failed` 通知。领取前失败不会通知或创建 execution。schema 3 的每条通知包含
`fixtureId`、`materialId`、`executionId`、`connectionId`、`command`、
`execution` descriptor 路径、`receipt` 和持久 `result` 路径；mock 还包含
`processReceipt`。通知包含不可变 descriptor 的 `executionSHA256`；成功 record 的最终通知和所有 mock
通知中的 `material` 包含精确 material ID、plan、manifest、raw 哈希，
不包含本机 material 路径。record 提交前失败不把未发布材料当成成功引用；publicationCommitted 区分提交前失败
与已提交后的通知/清理失败，不能把后者解释为 current 仍是旧材料。

即使不传 `--result-fd`，同一结果仍保存在
`packages/<materialId>/executions/<executionId>/receipts/<connectionId>.result.json`。
结果版本及路径基准遵循[路径相关证据](design.md#路径相关证据)和
[schema 版本与兼容](design.md#schema-版本与兼容)。通知与落盘结果完全一致；
消费者用本次传入的 fixture 根和通知中的 material ID 定位
`<fixture>/packages/<materialId>/`，再把通知中的 package 相对路径拼到该包根。
不得把 package 相对引用改成 fixture 根引用，也不得将相对路径直接交给依赖宿主
cwd 的文件读取操作。running 通知已经包含 material ID，因此 execution 尚未派完
所有连接时也能定位当前 package。
结果通知通过本次进程明确关联身份，调用方不得扫描最新目录来猜测归属。
单个 connection 的 `passed` 不代表同一 execution 的其他 connection 已完成；
完整状态以 execution index 和各 receipt 为准。多连接调用分别使用独立结果描述符。
通知写入响应取消，每条通知最多等待 1 秒；
管道无人读取或已关闭时命令失败，持久结果文件仍可读取。最终通知在取消后仍作
最多 1 秒的有界写入。描述符只用于通知，不得与协议或其他输出共享。
launcher 只转发原生参数；需要结果通知时，使用 route 的 `command` 和 `args`
直接启动，并在原生参数分隔符之前传 `--result-fd`。

record 在采集、材料闭包和清理完成后发布 current，并返回自身结果，不等待父 SDK
宿主的业务报告。case source 的直接 agent record 也只代理真实 Agent；其材料保持
external provenance，官方业务事实由 official 编排另行保存和核验。

## 使用 mock

```sh
agent-sdk-testify prepare \
  --recording /absolute/material --fixture-dir /absolute/fixture
agent-sdk-testify agent mock --fixture-dir /absolute/fixture
```

材料的 consumer provenance 不因导入或导出而提升。只有 prepare 接受材料路径，mock
只接受 fixture 和本次执行参数。

消费者仍负责 SDK 调用、业务断言、策略比较和真实 E2E。`official verify` 只接受官方 driver
和官方材料，不用于 consumer material。

## 可重复性

consumer recording 是字节级录制，不是语义模拟。回放要求消费者再次发送同样的客户端
帧；只有 Codex JSON-RPC ID 和 Claude control request ID 按既有 adapter 规则重绑定。
工具不提供 UUID、时间戳、cwd、业务 ID 或任意字段的通配匹配，也不会自动搜索其他
录制或回退到真实 Agent。

如果消费者每次运行生成新的 UUID、session 标记或其他输入字段，必须先提供确定性的
测试模式，或改用消费者自己的 stateful peer。重新录制一次不能解决运行间差异。
consumer material 至少应连续回放两次并保持严格匹配，才能作为该消费者的回归基线。

## 证据与发布

plan source 的 record 只报告本次采集的完整性：真实 Agent 已启动、客户端 EOF、帧和
哈希完整、进程退出和工具自有资源清理成功。它不报告官方 SDK 业务结果、产品 E2E
通过或覆盖完整。

consumer material 归消费者保存和发布；本仓库不写入 `data/index.json`，不生成内置
快照。需要官方采用时，必须用 `prepare --case` 和 `official record` 重新采集，再走 `official verify`、
资格审核和采用，不能把 consumer material 提升为官方材料。

## 安全与失败

- fixture 目录和 runtime 使用私有权限；`stderr.raw` 可能包含提示词、认证错误或环境
  信息，材料默认保持私有，不进入公开归档。
- 只从 agent type 标准配置位置复制允许的原生配置和认证；不读取任意配置路径，不复制
  个人配置、项目指令或无关会话。
- prepare、record 或 mock 失败都保留各自的失败证据和诊断；
  不得把部分材料标记为成功。
- 真实 Agent 的启动和网络访问只发生在显式 record action 执行期间；
  默认测试使用受控子进程，不连接真实 Agent。

## 实现边界

plan source 复用 `internal/protocol/capture` 的 stdio 采集和进程生命周期，不新增第三套
协议引擎；准备层负责隔离环境、配置、fixture、历史种子和 material 布局。`agent mock` 的
严格回放、fixture 生命周期和回执语义不变。

默认测试应覆盖 prepare 的环境/workspace/config、fixture/execution descriptor 不可篡改、
source 互斥、绝对路径、plan/agentType 不一致、原生参数白名单、execution 的帧、失败证据和历史哈希、consumer 材料不具备官方 verifier、
`prepare/record/mock`、同一 fixture record→mock、历史种子 allowlist、符号链接拒绝，以及运行间 UUID 变化的
负向用例。真实 Agent 检查只作为显式 opt-in，不计入默认离线门禁。

## 非目标

- 不执行 HTTP/SSE 或非 stdio 协议。
- 不模拟录制中的 shell、文件编辑、hooks 或模型调用及其副作用。
- 不安装真实 Agent、不执行登录或认证流程；只读取 agent type 标准配置位置和当前进程
  中已存在的允许项。
- 不启动消费者自己的 SDK 宿主；消费者应用运行时 route 后自行启动。
- 不把 consumer material 变成官方 case、官方验证结果或发布材料。
- v1 不实现 `probe` 或其他多连接录制。
