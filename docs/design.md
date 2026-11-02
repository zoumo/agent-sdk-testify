---
type: Design
title: agent-sdk-testify 独立拆分与三项能力
status: approved
---

# agent-sdk-testify 独立拆分与三项能力

本文设计已获用户批准。已从源仓库建立原样复制基线，三命令收敛尚未实施；来源为 `agent-sdk-go`
的 `428263bd594bcd2586049ba38d01c5573284e0ca`。本文中的命令为目标接口，
不能作为已实现、已发布或 SDK/MASS 验收通过的证据。

[查看目标架构图](../.archify/architecture-three-commands-20261102-181131/extraction.html)。

## 1. 核心边界

`agent-sdk-testify` 对外只提供三项业务能力：

| 命令 | 职责 | 执行方与对端 | 输出结论 |
| --- | --- | --- | --- |
| `record` | 按本仓库 case 录制真实 Agent 交互 | 本仓库官方 driver → 官方 SDK → 录制代理 → 真实 Agent | 本次业务执行、采集完整性、退出和清理结果 |
| `verify` | 用官方 SDK 验证已有录制 | 本仓库官方 driver → 官方 SDK → 内部严格回放引擎 | 官方业务断言、协议匹配、完整消费、EOF 和清理是否通过 |
| `mock` | 使用录制充当 Agent | 消费者自己的测试 → 被测 SDK → mock | 每连接的协议匹配、完整消费、EOF、退出与工具资源清理结果 |

`verify` 对应当前实现中使用官方 driver 的 `replay`，名称明确表示“验证录制”。
它不是对消费者测试结果的复核，也不启动真实 Agent。
`simulate` 留在 `agent-sdk-go`，负责该 SDK 的流程执行、业务断言、批量运行和覆盖汇总。
本工具不提供 `simulate`，不接收或启动 Go SDK driver，不替 SDK 或 MASS 推进业务测试。

case、provider binding、输入/预期、覆盖要求、官方录制 driver、录制材料、固定索引和发布集合
均在本仓库维护。`agent-sdk-go` 与 MASS 都是 mock 消费者，不是本工具的材料上游。
第一版支持 Codex/Claude 的 JSONL 原生协议，消费者的实现语言不受 Go 限制。

## 2. 仓库归属

| 内容 | agent-sdk-testify | agent-sdk-go |
| --- | --- | --- |
| case 与能力要求 | 36 个 case、binding、输入、预期、required/optional、通用目标证据规则 | 实现 Go SDK 对 case 合同的操作与断言 |
| 录制与官方验证 | 官方 driver、Python wheel、真实录制入口、官方回放验证 | 消费已发布能力，不维护另一套官方录制流程 |
| 材料 | 私有原件归档、审核采用录制、固定索引、来源与哈希、内置集合 | 不维护录制库副本；保存自身测试报告 |
| 协议与环境 | 录制代理、严格匹配、ID 关联、历史释放/派生、隔离目录及协议回执 | 将工具给出的入口、环境和目录配置到 SDK |
| SDK 仿真 | 提供 mock、场景合同和材料可用性 | `simulate`、Go driver/测试适配、并行/超时、业务断言、SDK 覆盖门禁 |
| 测试 | 协议/环境自测、case 政策与材料资格检查、官方 driver 离线测试 | 当前 Go SDK 对接 mock 的集成、异常和清理测试 |

本仓库定义 case 要求并报告材料是否可用；SDK 判断本轮实现是否满足这些要求。
例如 case 028 的证据规则由工具维护，Go SDK 如何操作并满足规则由 SDK 测试实现。
内置成功子集不能替代完整覆盖，SDK 不能把缺失或未验证项算作通过。

## 3. 三个命令的接口

下面的路径与参数表示目标合同，均尚未实现。

### record：录制

```sh
agent-sdk-testify record --provider codex --case SDK-CASE-008 \
  --cli /absolute/codex --config /absolute/selected-config \
  --output /private/new-capture
```

工具选择本仓库 case 及官方 driver，准备隔离环境，调用官方 SDK 对接真实 Agent，
保存原始双向帧、stderr、原始 plan、来源 manifest、业务报告、采集及清理结果。
只有 record 可以连接真实 Agent，须由操作者显式执行；离线检查不自动录制。
第三方官方 SDK 依赖在录制环境显式安装，不在运行时自动安装，也不要求 mock 消费者安装。

record 成功只代表本次录制流程达到其业务、采集与清理要求；新录制仍需 verify。
失败、未触发和不支持如实保存，不能靠录制文件存在就判定 case 成功。

### verify：官方 SDK 验证录制

```sh
agent-sdk-testify verify --recording /private/new-capture \
  --output /private/new-verification
```

工具由录制身份确定 provider/case，使用本仓库对应的官方 SDK driver 执行同一 case，
对端使用同一个严格回放引擎，不连接真实 Agent。verify 不接受任意被测 SDK driver。
官方 driver 是 record/verify 的内部执行机制，不是消费者接入工具的要求。

通过须同时满足：来源/文件哈希有效、官方业务断言通过、所有选中连接严格匹配并完整消费、
EOF/退出正常且自有进程清理成功。业务通过但协议回执缺失或采集不完整，仍然失败。
结果绑定录制/合同哈希、官方 SDK 版本、工具版本及各连接回执，保留失败诊断。

verify 不改写原始录制；历史需要运行路径时按既有规则派生副本并记录关联。
通过 verify 后，结合既有内容/凭据/来源审核执行采用与发布资格检查。
资格、归档、原子入库属于内部材料管理，不新增顶层 corpus 命令。
SDK 当前仿真结果不作为录制采用条件，SDK 失败不得导致删除可信录制或放宽协议匹配。

### mock：消费者的原生 Agent 对端

普通场景可直接替换 Agent executable：

```sh
agent-sdk-testify mock --provider codex --case SDK-CASE-008 \
  --report /reports/mock-001.json
```

默认从工具内置集合选择材料，不需要消费者下载或理解录制目录。
保留显式 `--recording /absolute/capture`，用于材料发布前验证、私有材料及专项测试。
内置入口和外部入口使用相同的严格匹配、完整消费和失败语义；未完成资格的外部材料
不因可运行而获得“已验证/可发布”身份。未发布 case、缺文件或哈希漂移明确失败。

场景查询、父进程环境准备和收尾均属于 mock 的配套操作，不扩成新的顶层业务命令：

```sh
agent-sdk-testify mock --list --provider codex --json
agent-sdk-testify mock --describe --provider codex --case SDK-CASE-008 --json
agent-sdk-testify mock --prepare --provider codex --case SDK-CASE-008 --output /reports/run-001
agent-sdk-testify mock --run /reports/run-001/run.json
agent-sdk-testify mock --finish --run /reports/run-001/run.json
```

这些模式互斥；参数 schema 与错误分类在实现时固定为 v1。默认启动模式的 stdout 仅为
原生协议，其他模式的结构化输出与协议进程分开，诊断用 stderr 或报告文件。

- list/describe：提供实际发布的场景、完整覆盖要求及材料缺口、输入顺序、审批选择、
  原生参数、环境前提、支持范围及限制；不会启动真实 Agent。
- prepare：创建自有隔离资源，保存 `schemaVersion: 1` 的 run.json，返回 workspace、
  所需 env、各连接 command/args、必要的无参 launcher、合同与回执路径，以及资源清单。
  准备完整覆盖清单不要求把未合格的录制发布为成功场景。
- run：绑定已有 run，启动对应协议对端。多连接场景分别分配入口和独立回执。
- finish：在消费者关闭 Session/Runtime 后核验所有选中连接的材料身份、严格匹配、完整消费、
  EOF 与退出；随后清理该 run 自有资源并报告两类结果。协议失败也尽力清理，缺失回执不能
  被 cleanup 成功掩盖。可重复收尾，持久报告及消费者资源不删除。

简单直接启动的 mock 在 EOF/退出时完成自身回执与清理；需要父进程共享目录、初始文件或
多连接的场景必须先 prepare，并由消费者最终 finish。不能用业务 Completion 代替 Session 结束。
`verify` 始终专指官方 SDK 验证录制，mock 的收尾核验不再使用 verify 命令名。

## 4. SDK 和 MASS 如何消费

`agent-sdk-go` 的 simulate 是 SDK 自己的测试入口，可用现有 Go driver 或直接测试代码实现。
它只通过 mock 的进程接口、场景描述、运行描述及回执工作，不调用工具的旧 simulate/replay。
Go SDK 自己管理业务测试、驱动进程、并行、取消、报告和覆盖门禁；工具管理各 mock 的资源。
SDK 自己需要的 WebSocket 服务和 Go SDK preserve/suppress 对比执行也归 SDK 编排，
每个协议对端仍通过 mock 获取独立入口与回执。协议桥接实现可以复用，但不新增顶层 connect。

SDK 和 MASS 均遵循以下顺序：

1. 读取 mock 场景描述，确认输入、握手、原生参数和能力兼容。客户端身份或业务参数不同
   不靠修改录制、删帧或忽略字段解决；不兼容须报告。
2. 有环境前提时调用 mock prepare，启动 SDK 宿主前应用返回的 env/workspace/入口。
3. 从自己的公共接口执行输入、审批等操作，断言自己的 SDK/产品结果。
4. 关闭自己的 Session/Runtime，等待 mock 完成，读取协议回执或调用 mock finish。
5. 清理自有服务、端口及业务资源，保留业务与 mock 报告。

最终通过条件是业务断言通过、所有 mock 对端有效完成、双方各自资源清理成功。
MASS 不必实现 driver，SDK 也可以直接使用自己的测试框架。

## 5. 材料、环境与保存位置

材料链路：本仓库 case → record → verify → 资格/内容审核与采用 → 固定索引 →
内置发布集合 → SDK simulate 或 MASS 测试 → mock。

| 材料 | 保存位置与规则 |
| --- | --- |
| 完整原始采集包 | 本工具管理的 Git 外私有归档，保留成功/失败、原件身份和哈希 |
| case、审核采用录制、固定索引 | 本仓库的权威材料，迁移保留原始字节及历史来源 |
| 官方验证结果 | 与 attempt/原件关联，和原始录制分开保存，不改写旧证据 |
| 内置快照 | 仅合格、官方业务验证 passed 的允许发布材料，随工具版本固定 |
| 发布清单 | 固定来源 revision、provider/story/case/attempt、合同及文件哈希，构建自动生成 |
| 本次 workspace/HOME/config/tmp | 独立可写临时资源，由 mock 记录拥有关系并清理 |
| 本次运行证据 | 环境描述、派生关联、各连接回执、失败诊断及清理结果，独立持久保留 |

采用成功不单独证明可发布。发布构建拒绝缺失、哈希漂移或验证非 passed 的默认成功场景；
完整固定索引继续显示失败和未验证项，不能静默只报告成功子集。没有合格材料须报告空集合。

工具准备 HOME、workspace 和 tmp；Codex 准备 CODEX_HOME 及必要配置/历史，Claude 准备
CLAUDE_CONFIG_DIR 并按既有顶层 cwd 规则派生历史。消费者必须在启动 SDK 宿主前应用配置：
mock 子进程无法修改父进程环境。Codex 原始协议 cwd 与可写 workspace 按原生身份规则区分。
仅配置到 mock 子进程的目录不足以支持父进程 SDK 的历史查询。

不复制完整用户环境、个人 skills、插件、记忆或认证进发布材料；保持既有审核和凭据保护，
不新增脱敏副本、宽松字段替换或发布后扫描流程。原始 raw/plan 不变，历史派生保留双向哈希关联。
mock 不执行录制中的 shell、文件编辑或模型调用，不能据此推断工具副作用被重现。

## 6. 迁移映射

| 当前入口或职责 | 目标归属 |
| --- | --- |
| `record` 与底层 `protocol record` | 工具 record，录制代理为内部实现 |
| 用官方 driver 执行的 `replay` | 工具 verify，不保留顶层 replay 别名 |
| `protocol mock`、材料准备与协议回执 | 工具 mock 及其配套操作 |
| `simulate`、Go driver、SDK 双策略/远程流程、SDK gate | agent-sdk-go 的 simulate 与测试代码 |
| `corpus review/qualify/adopt` | 工具内部材料校验、归档和采用机制，不再单列公开命令 |
| `corpus gate` | 拆分：材料完整性/资格在工具；当前 Go SDK 执行与覆盖门禁在 SDK |
| `protocol connect` | 按使用方职责保留内部协议桥接或 SDK 测试适配，不作为第四个工具命令 |
| 顶层 scenarios/prepare/cleanup、原拟议 `verify --run` | 收拢到 mock 查询/准备/收尾；verify 只用于官方验证录制 |

case 流程如 existing-children 由本仓库维护其合同及官方执行；Go SDK 如何执行由 SDK 负责。
不把 Go SDK 的私有流程控制当成工具公开合同，不为拆分添加动态插件、通用工作流 DSL 或嵌套 module。

目标布局（源码直接位于独立仓库根目录，不保留原 SDK 的 tools/agent-sdk-testify 包装目录）：

```text
agent-sdk-testify/
  go.mod / go.sum / mise.toml
  main.go / record.go / verify.go / mock.go
  protocol/                 # 内部录制、严格回放与传输实现
  scenarios/                # 权威 case、binding、输入与预期
  drivers/                  # 官方 driver、证据规则与 Python wheel
  recordings/               # 审核采用材料、固定索引及来源
  builtin/                  # 生成的发布快照
  *_test.go / testdata/      # 工具自测与合成材料
  scripts/ / docs/ / README.md / LICENSE
```

单二进制 Go 工具的生产与默认测试依赖不得导入 agent-sdk-go。保留许可证和来源说明，
沿用 mise 固定工具链；官方 SDK 只在 record/verify 环境依赖中安装。

## 7. 实施里程碑与验收

### A：迁移归属，形成三个入口

1. 迁入协议、环境、录制、官方验证、case/binding、官方 driver/wheel、材料及固定索引；
   自身导入路径改为新 module，逐文件保留原件哈希和历史来源。
2. 将 Go SDK 仿真编排、Go driver 及相关集成断言归回 SDK。拆分当前 run/corpus/peers 职责，
   不能直接把整个原运行器迁入工具后保留 simulate。
3. 工具只暴露 record/verify/mock，官方驱动作为内部机制；SDK 脚本只构建自己的测试适配，
   通过明确的外部工具路径消费 mock。官方 wheel 在本仓库构建。
4. 更新两仓测试、CI、文档、依赖检查及来源证据，保留错误/来源/多连接回执/清理断言。
5. 使用本地二进制做跨仓库验收；正式 SDK CI 切换到已发布的固定工具版本后，再删除 SDK
   内重复工具、case 和录制维护入口。不留下不可获取的 pin 或破坏唯一可用测试入口。

验收：工具 UT、受控进程集成、race、vet、依赖边界及源码目录外运行通过；
CLI 业务入口只有三项；case 数、政策、索引身份和原件哈希迁移前后一致；官方验证无需
Go SDK checkout；Go SDK 的 simulate 能调用外部 mock 完成原有测试且保留失败证据。
真实录制默认不执行，合成验证不能冒充真实样本验证。

### B：内置材料与 mock 消费

1. 从本仓库固定索引生成合格发布集合，绑定合同/原件哈希和工具版本；保留外部材料验证入口。
2. 实现 mock 的场景查询、准备、直接启动和收尾，内置/外部入口共用协议及资源逻辑。
3. 验证缺回执、未消费、异常退出、哈希漂移、多连接及清理失败，确认报告持久保留。
4. 使用已合格且兼容的真实材料验证离开源码目录的 Go SDK simulate；MASS 用自己的业务
   测试消费同一 mock 能力，分别报告业务与协议结果。

验收：标准工具内置集合确定且非空；消费者无需外部录制目录或官方 SDK；父进程历史配置
可正确应用；模拟期间不启动真实 Agent；SDK 完整覆盖仍报告所有缺口。
无合格或兼容材料时报告未验证，不新增真实录制、不放宽匹配以完成迁移。

工具版本由 SDK 中单一版本文件固定，CI 在准备阶段安装，然后离线测试。
本地优先使用绝对路径 AGENT_SDK_TESTIFY_BIN，否则发现 PATH 中符合固定版本的工具；
缺失时明确失败，不自动构建 SDK 内源码或安装 driver。运行证据记录实际工具版本/哈希
以及 SDK 自身 revision，二者不混记。标准版本提供 Linux/macOS 产物及校验和。

## 8. 本次审批内容

请审批以下边界及配套接口：

- agent-sdk-testify 只提供 record、verify、mock 三个业务命令，verify 使用官方 SDK 验证录制。
- case、官方 driver、录制及材料库归本仓库；simulate、Go driver 和 SDK 执行门禁归 agent-sdk-go。
- mock 负责协议对端及必要环境/回执；查询、准备、收尾作为 mock 的配套模式，消费者负责业务测试。
- 按 A/B 两个里程碑实施，分别报告边界迁移和内置消费验收。

上述设计已批准。当前先按[实施规格](implementation-plan.md)建立复制基线；
不修改源 SDK，不自动录制、提交、推送或发布版本。
