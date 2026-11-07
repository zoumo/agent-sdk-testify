# 开发规范

## Module 与依赖

使用单一 module `github.com/zoumo/agent-sdk-testify` 和单一工具二进制，
使用 Go 1.26 和 golangci-lint 2.12.2；不添加嵌套 module、replace 或 go.work。
生产代码及默认测试不得导入 agent-sdk-go、Go driver 或 SDK fixture 容器。
官方 SDK 仅作为 official record/verify 的显式环境依赖，不在运行时自动安装。

CLI 消费者通过 `prepare --plan` 和 record 采集、mock 进程、会话/运行描述与独立
回执接入。模块根包是可导入库包，公开 API 以
[可导入 Go 库入口规格](library-entry-spec.md)为准。
公开 CLI 结构和职责以[工具设计](design.md)为准。agent record/mock 不依赖官方
SDK；official record/verify 通过官方 driver 调用同一套公开 agent 命令。
材料资格、归档、采用和远程传输为内部能力，不提供独立 internal-protocol 入口。

## 代码布局

```text
agent-sdk-testify/
  go.mod / go.sum / Makefile / .golangci.yaml
  doc.go                    # 可导入库包 agenttestify 的入口
  cmd/agent-sdk-testify/
    main.go                 # CLI 入口
  internal/
    lifecycle/              # prepare 与会话/execution 状态
    record/ mock/           # agent 协议执行
    catalog/ corpus/        # case/catalog 与材料资格、采用、索引
    builtin/ official/      # 内置快照与官方录制/验证编排
    native/                 # 原生 Agent 配置、环境、历史种子与服务准备
    history/ tool/          # 历史派生与共享基础能力
    protocol/               # 采集、严格回放、录制格式与传输；仅供仓库内部使用
  data/
    scenarios/              # 权威 case、binding、输入与预期
    claude/ codex/          # 审核采用的 agentType/case/attempt 材料
    index.json              # 固定索引
    builtin/                # 生成的发布快照
  tests/intergration/       # 多模块集成测试
  test/e2e/                 # installed CLI 的完整流程测试
  tests/build/              # 生成器等独立构建检查
  drivers/                  # 官方 driver 与 Python wheel
  docs/ / scripts/ / README.md / LICENSE
```

布局变更时同步 imports、embeds、脚本、测试及文档。不要添加无需求的 pkg、
框架、转发层、动态插件或通用流程 DSL。

协议包不依赖 case 编号、driver 或 SDK。capture 拥有自己的 I/O、选项和类型化错误，
命令成功退出 0，失败统一退出 1，并在 stderr 输出具体原因；业务包直接返回 error，
不维护退出码分类。agent type 的配置、启动参数和历史解释留在对应 agent 文件。
端到端测试使用与消费者相同的进程接口，不导入被测 SDK 来间接证明工具正确性。

协议代码统一位于 `internal/protocol/`，保留 capture、recording、replay、rawjson 的
职责边界，不提供跨仓库 Go API。command 只解析参数并调用内部能力，不增加重复的
选项转发层。official 编排、driver 和公开 agent 命令的职责以
[工具设计](design.md#记录验证与回放)为准。

### 内部职责与依赖

- `record`、`mock` 是公开 agent 命令的策略适配器：`record` 只提供录制准备、采集和
  材料打包，`mock` 只提供回放。`lifecycle` 拥有一次 action execution 从开始或加入
  到结果交付和释放锁的完整事务，包括连接 claim、overlay、index 转换、收尾串行化、
  route 恢复、材料发布和 current 切换。`official` 集中官方录制与官方验证编排，
  不保留独立 `verify` 包或普通录制包中的官方编排入口。
- `native` 集中原生配置过滤、认证环境、启动准备、历史种子准备及工具拥有的原生
  服务管理。它不依赖 session、execution 或官方编排；session 的环境基线、overlay
  路由、资源归属、锁和清理时序仍由 `lifecycle` 管理。driver 发现、请求、launcher、
  subprocess 与证据 plumbing 归 `official`，由同一 driver-run 统一拥有。
- `lifecycle` 不依赖 `official`。官方编排通过 `lifecycle` 管理活动登记和精确
  execution 引用，通过 `native` 准备原生环境，driver 继续调用公开的 `agent record`
  和 `agent mock` 命令。
- 生产回放使用 `mock` 到 `replay` 的执行路径；协议根包保留原生启动参数校验，
  不维护只供测试使用的第二套录制加载、超时或 receipt 流程。
- 材料自身的格式、哈希闭包及来源规则归 `corpus`，case/binding 与 plan 合同归
  `catalog`，内置快照校验归 `builtin`。`lifecycle` 保留材料与当前 session、source、
  execution 的关系校验、current 选择和原子发布；官方 verifier 的选择归 `official`。
  材料相关包不得反向依赖 `lifecycle`，也不新增通用校验框架。
- 以上内部调整保留公开 CLI、持久数据合同、严格回放、来源原字节和失败证据。
  测试迁移必须保留原有行为覆盖；协议专项可以使用受控内核进程，纵向验收仍通过
  installed CLI，不为测试保留第二套产品实现。

## 材料与测试输入

原件及失败尝试保存在 Git 外私有归档。已跟踪材料按 agentType/story/case/attempt 归属；
固定索引保留所有要求、失败和缺口。移动不改变原始 plan/raw、attempt 身份或来源哈希。
派生运行副本须保留原/派生路径及双向哈希关联，不改写原件或放宽协议字段。
consumer material 保持在消费者私有目录，不登记固定索引、不进入内置快照。
材料发布须遵守[工具设计](design.md#信任与入仓)。

合成输入只用于专项测试，不能登记为真实录制或用户故事成功证据。

完成 verify、资格审核和采用后，用材料所在的完整 40 位源码 revision 重新生成内置
快照；普通版本变更也需要重新生成：

```sh
go run -tags builtin_generate ./cmd/agent-sdk-testify --revision FULL_40_CHARACTER_SOURCE_COMMIT
go run -tags builtin_generate ./cmd/agent-sdk-testify --check
```

生成器从固定索引选择 `admitted` 且 `verification=passed` 的材料，校验完整闭包并
复制原始字节。缺文件、哈希漂移、空选择或生成物过期必须失败；`scripts/build.sh`
和默认测试都会执行 freshness check。

## 编码约定

使用清楚的名称和语言惯用写法；注释解释原因，避免重复代码。
复用解决实际重复，性能优化需要实际需求。第三方依赖须有明确用途、版本与来源。
限制 lint 例外并说明原因；lint 不替代编译、测试和 race。
协议 stdout 与诊断分离，不记录认证秘密；可复用模块不修改全局日志或遥测配置。

## 测试

测试分为三层，按测试范围和目录归类。单元测试和集成测试应快速完成，适合开发时频繁运行；
完整流程只在端到端测试中验证。
[共同验收](cases.md)定义 8 个用户故事、36 个 case；agent binding 的
required/optional、planned/unsupported 及原因必须保留。

| 层级 | 位置 | 测什么 | 执行边界 |
|---|---|---|---|
| 单元测试 | 与被测代码放在一起的 `*_test.go` | 单个模块的规则，如协议匹配、哈希、字段校验、配置和历史派生 | 直接调用函数，不启动 CLI、SDK、Agent 或子进程，不访问网络 |
| 集成测试 | `tests/intergration/` | 多个真实模块之间的协作，如材料加载与校验、采集与回放、状态与回执 | 可以使用临时文件、本地管道和回环连接，必要时 mock 外部依赖；不走完整 CLI 流程 |
| 端到端测试 | `test/e2e/` | 消费者实际使用的完整流程，如 prepare → record → mock | 使用实际构建的 CLI，通过 argv、stdio、退出码、descriptor、文件和哈希检查结果，不直接调用 lifecycle 函数 |

集成测试只有在验证 EOF、退出、取消或清理等进程行为时才启动必要的受控子进程。
受控进程调用真实 capture/replay 内核，不复制产品实现，不增加测试专用产品入口。
mock 只替代测试范围以外的依赖，不替代本次要验证的模块协作或严格协议回放。
SDK 公共接口、消费者业务断言及 simulate 由消费者仓库负责。

### 速度与覆盖

- 单元测试枚举字段、错误和边界组合；集成测试检查模块确实连接正确；端到端测试
  保留关键流程和代表性失败，不在三层重复枚举同一组组合。
- 例如，哈希错误的各种组合在单元测试中覆盖；集成测试检查材料加载会执行哈希校验；
  端到端测试只检查代表性错误被 CLI 拒绝，且 current 不变。
- 单元测试和集成测试不得为每个 case 构建 CLI、准备完整会话或启动整套环境。
  不使用固定 sleep 等待成功，改为等待可观察事件，并为等待和清理设置上限。
  通过逐测试耗时定位慢项，优先消除重复构建、进程启动和无关准备。
- 端到端测试是唯一完整 CLI 流程验收入口，不在各业务包中重复维护纵向验收。
  同一种构建模式的 CLI 构建一次并复用路径；每个测试仍使用独立可写状态，
  不共享可写 session 或 execution overlay。
- 关键覆盖保留 record/mock、official record/verify、远程与多连接、export 后重新导入、
  调用方删除 session、取消及失败证据。分片、大消息、关联 ID、超时、取消、背压、
  历史派生、会话复用、overlay 隔离和资源清理按所属层覆盖，不全部提升为完整流程。
- 分层调整保留全部合同断言、严格匹配、来源原字节、哈希与资格政策；不能通过删 case、
  skip 或放宽 deadline 缩短耗时。构建、打包和安装检查单独报告，不计作单元或集成测试。

### race 与真实执行

race 是检查方式，不是第四层测试。单元和集成测试使用 `go test -race`；端到端测试
实际启动的 CLI 必须由 `go build -race ./cmd/agent-sdk-testify` 构建，仅给测试进程加
`-race` 不算覆盖 CLI。
复用二进制时须核验构建模式，受控 launcher 必须传递 race 配置；子进程报告数据竞争时，
即使该 case 预期业务失败，测试也必须失败。
完整门禁中的同一套 CLI 流程只需用 race CLI 跑一次；普通发布二进制另做少量启动和打包检查。

默认三层测试均离线，不启动真实 Agent、不需要认证、不安装官方 SDK、不新增录制。
端到端不等于真实 Agent 测试；需要真实 Agent 的场景仍放在 `test/e2e/`，仅显式授权执行，
保留 opt-in 门禁和默认 skip。合同和材料存在不证明当前 SDK 通过，合成材料必须保持
synthetic 身份。官方验证、SDK 仿真和真实 E2E 分别报告，不互相背书。

### 离线门禁

使用 Go 1.26。完整门禁由 `make check` 执行，覆盖格式、lint、独立构建、默认测试、race、
vet、依赖边界、源码目录外运行、driver 测试和 wheel 打包检查。
case/索引身份、全部录制字节、来源哈希和政策检查不得弱化。

材料缺失、失败和未验证须保持可见；内置成功子集不能抵扣完整 required 覆盖。
新样本须经官方验证及既有审核；Go SDK 失败不使可信录制失去资格，也不能触发删帧或
放宽匹配。正式消费者使用可获取的固定发布版本；本地显式二进制覆盖仅用于联调并
记录实际身份。

### 测试编写

- 使用 `testify/assert` 与 `require`；后续步骤依赖检查时用 `require`。共享生命周期
  用 `testify/suite`，单函数检查用命名表驱动 case，不叠加第二套生命周期或断言框架。
- suite 内不调用 `t.Parallel`；并发、synctest 和时序敏感覆盖保留原生 `testing`。
  worker goroutine 不得调用 `require`/`FailNow`，结果交回拥有检查的测试 goroutine。
- 保留全部 case、预期值、错误分类、未知字段、背压、取消和竞态断言；不得降低覆盖、
  放宽 deadline，或用 mock 替换录制协议回放来让变更通过。
- Go `Example`、`TestMain`/子进程、测试入口、helper、清理和显式 live skip 在需要
  原生 API 时保持原生。修改生成器后重新生成并验证，不只手改输出。
- capability、源码检查、离线/合成测试和真实录制证据分别保留，skip 不是 live pass。

### 环境与结果

测试使用自有临时资源，保留个人及其他任务的数据；失败时也释放己方资源并核对结果。
认证来自外部配置，日志和证据移除秘密。前置条件缺失或场景为空时报告未执行。
结果绑定实际源码、产物、环境和执行范围，保留失败证据；执行期间源码变化须说明。
通过、失败、未验证分别报告，筛选或局部通过不能代表全部通过，命令启动不代表完成。
