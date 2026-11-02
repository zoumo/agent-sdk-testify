# agent-sdk-testify 拆分实施规格

状态：设计已获用户批准；本轮已建立规划和源码复制基线（1,278 个受版本控制文件，35,860,852 字节），尚未完成三命令重构。

## Problem Statement

录制、官方验证、协议 mock、case 和材料目前位于 Go SDK 仓库，并与 Go SDK 的 simulate
编排混在同一个工具中。消费者难以只安装工具便获得稳定的场景与 mock；维护者也无法独立
演进 case、录制和发布材料。直接从空仓库重写会丢失成熟实现、测试断言与录制来源证据。

## Solution

从现有 Go SDK 仓库复制文档、harness、工具代码、case、已跟踪材料和官方 driver，建立
可校验的迁移基线；随后在该实现上收敛出 record、verify、mock 三项能力。
本仓库定义 case 并管理录制，Go SDK 的 simulate 留在 SDK，仅消费本仓库 mock。
完整沿用已批准的环境、协议、材料和清理合同，不重写已有匹配器或重新录制数据。

## User Stories

1. As a maintainer, I want to start from the existing implementation, so that extraction preserves proven behavior.
2. As a maintainer, I want copied files bound to a source revision and hashes, so that I can audit every imported byte.
3. As a maintainer, I want existing harness and glossary documents imported, so that the project retains its terminology and contracts.
4. As a maintainer, I want the approved design to override inherited ownership rules, so that old simulate responsibilities do not re-enter the tool.
5. As a case author, I want to define cases in this repository, so that all consumers use one authoritative contract.
6. As a case author, I want provider bindings and required/optional requirements preserved, so that extraction does not weaken coverage.
7. As a recording operator, I want record to execute cases through official SDKs, so that recordings capture real native interactions.
8. As a recording operator, I want isolated environments and explicit credentials, so that recordings exclude personal configuration.
9. As a recording operator, I want failed attempts preserved, so that capture failures remain diagnosable.
10. As a recording operator, I want verify to exercise recorded cases through official SDKs offline, so that usable recordings are distinguished from mere capture files.
11. As a verifier, I want every selected connection checked for matching, complete consumption, EOF and cleanup, so that partial success cannot pass.
12. As a verifier, I want original bytes and derived-history hashes retained, so that runtime preparation cannot silently rewrite evidence.
13. As a material maintainer, I want qualification and admission retained internally, so that removing public corpus commands does not lose material integrity.
14. As a material maintainer, I want failed and unavailable cases visible in the full inventory, so that a published subset does not hide coverage gaps.
15. As a release maintainer, I want only qualified materials embedded, so that a tool version has a trustworthy scenario set.
16. As a release maintainer, I want deterministic material manifests, so that consumers can reproduce the same scenario identity.
17. As a Go SDK developer, I want simulate to remain in the SDK, so that SDK workflow and business assertions evolve with its implementation.
18. As a Go SDK developer, I want mock available as a replaceable native executable, so that my existing SDK tests can use it directly.
19. As a Go SDK developer, I want independent receipts for preserve/suppress and remote connections, so that SDK orchestration does not hide incomplete runs.
20. As a MASS developer, I want my own business tests to connect to mock, so that I do not need a tool-specific driver.
21. As a consumer, I want scene descriptions and startup requirements exposed through mock, so that I need not understand recording storage layout.
22. As a consumer, I want parent-process environment preparation available before SDK startup, so that history APIs use the correct directories.
23. As a consumer, I want a launcher when my SDK accepts only an executable path, so that mock remains usable without custom scripts.
24. As a consumer, I want strict mismatch diagnostics, so that incompatible handshakes are reported without weakening matching.
25. As a consumer, I want mock finish to check receipts and clean owned resources, so that success and cleanup are assessed separately.
26. As a consumer, I want reports retained after cleanup and failures, so that test evidence survives temporary-directory removal.
27. As a consumer, I want private external materials supported, so that I can verify new recordings before they are embedded.
28. As a consumer, I want runtime use to require no source checkout or official SDK installation, so that mock is easy to deploy.
29. As a test maintainer, I want existing regression assertions preserved at the correct owner, so that extraction does not lower confidence.
30. As a CI maintainer, I want a pinned external tool version, so that SDK checks do not rely on adjacent checkouts or hidden local builds.
31. As a maintainer, I want offline checks separated from live recording, so that routine validation never spends model calls.
32. As a maintainer, I want the source SDK left intact until replacement checks pass, so that extraction remains reversible.

## Implementation Decisions

- 采用复制后收敛。首轮只导入受版本控制的相关文件，保留目录布局、源 revision、文件哈希及已有测试；不从空目录重新实现。
- 三个公开业务入口固定为 record、verify、mock。当前官方 replay 改为 verify；不保留工具 simulate/replay 或公开 corpus/protocol 命令组。
- case、provider binding、目标证据规则、官方 driver、官方 wheel、录制库和固定索引归工具仓库；Go SDK runtime、Go driver、simulate、SDK 业务覆盖门禁归 SDK。
- record 与 verify 复用现有官方 driver 及进程请求合同。它们不接受任意被测 SDK driver，不因沿用内部驱动机制而保留通用 SDK 测试运行器。
- 从现有运行器中按职责提取录制、官方验证与 mock 准备；Go SDK 双策略比较、SDK 远端业务流程及并发门禁留给 SDK simulate。保留协议连接的独立回执。
- mock 是消费者唯一的测试接入点。场景查询、环境准备、启动和收尾都收敛在 mock 配套模式；verify 只指官方 SDK 验证录制。
- 保留单 Go module 和单工具二进制；沿用固定 mise 工具链和许可证。module 名称及内部导入路径在基线复制之后统一切换，不以 replace 或 go.work 掩盖依赖。
- 生产及默认测试依赖不得导入 Go SDK。源实现中依赖 Go SDK fixture/driver 的集成测试需拆回 SDK，协议和材料断言保留在本工具。
- 官方 SDK 依赖只服务 record/verify 环境，mock 消费者不需要安装官方 SDK、Go 编译器或源仓库。
- 完整私有原件继续在 Git 外归档；只复制源仓库已跟踪的录制。复制不提升其采用、验证或发布资格，不扫描、脱敏、改帧或重新录制。
- 原始 plan/raw、attempt 身份、原件哈希及历史派生规则保持不变。新的工具身份与历史采集身份分别记录，不把旧来源伪装成新采集。
- 材料资格、归档、原子采用和发布闭包继续存在，但作为内部材料管理。SDK 仿真通过不是可信录制采用条件。
- 内置集合由本仓库 case、固定索引及合格录制构建，工具版本固定其哈希；外部材料入口继续支持发布前验证及私有材料。
- SDK/MASS 在启动宿主前应用 mock 返回的 workspace/env/launcher。mock 不能修改父进程环境，消费者自行推进业务并关闭 Session/Runtime。
- mock 收尾核验所有连接并清理自有资源；业务失败与协议失败均保留诊断。工具清理不删除持久报告或消费者数据。
- SDK 固定已发布工具版本；本地显式二进制覆盖可用于联调并记录真实身份。正式 CI 不依赖相邻 checkout，不自动连接真实 Agent。
- 继承的 harness 包含 SDK 合同及旧命令，属于待收敛基线。先保留原件，后按已批准设计修订规范与入口；已批准的归属优先于继承文字。
- 本轮不删除源 SDK 文件，不提交、不推送、不发布版本。完成固定版本及跨仓库替代验收后，才在后续变更中移除旧维护入口。

## Testing Decisions

- 用户已批准的最高接入点是工具进程接口：record/verify 的官方执行及 mock 的原生 stdio、环境描述和回执；继续使用现有接口，不新增跨仓库 Go 库耦合。
- 测试可观察行为：CLI 输入/输出、协议匹配、业务验证结果、完整消费、EOF、进程退出、目录清理、持久诊断和文件身份。避免只检查内部函数调用次数。
- 首轮复制逐文件对比源 revision 的内容哈希、模式和文件清单；检查 case/索引数和录制字节，不把复制成功写成仿真通过。
- 延续现有协议测试矩阵：关联 ID、未知字段/数字字面量、严格握手、错误分类、部分消费、取消、受控子进程及多连接回执。
- 延续工具在源码目录外通过自身二进制启动协议子进程的专项测试，随后改为验证三命令对外合同。
- 延续官方 driver 的离线材料、配置证据、审批、历史、远端连接等测试。人工构造的材料保持 synthetic 身份。
- 依赖 Go SDK 的请求接入、历史派生消费、SDK 门禁失败保留等断言转由 SDK 进程集成测试承接，不删除或标为普通跳过。
- 先跑可独立执行的协议/材料检查；根 module 与 SDK 测试拆分完成后，运行工具完整 UT、受控集成、race、vet、依赖及打包检查。
- 使用同一材料验证内置/外部 mock 入口严格性、缺回执、未消费、异常退出和清理语义一致。
- Go SDK simulate 与 MASS 测试分别验证自己的业务结果及 mock 报告；二者任何一方通过都不能替另一方背书。
- 迁移已有真实录制后独立运行官方 verify 和 SDK 仿真，保留原有失败/缺口。空集合、缺材料及兼容性问题均明确报告。
- 默认所有验证离线，不执行 record、不启动真实 Agent、不新增录制。官方真实录制不是本轮复制或常规 CI 的门禁。

## Out of Scope

- 从零重写协议匹配器、官方 driver、case 定义或材料管理机制。
- 将 Go SDK 的运行时或 simulate 编排迁入工具，或增加第四个公开业务入口。
- 修改旧录制帧、身份或哈希来掩盖兼容性问题；弱化 required/optional、完整消费或清理门禁。
- 自动执行真实 Agent、模型请求、新录制、远程部署、提交、推送或版本发布。
- 首轮复制中删除源 SDK 的任何代码、case 或材料。
- 动态插件、通用测试工作流 DSL、任意新 Agent 协议、嵌套 module 或独立远程材料服务。

## Further Notes

### 执行顺序

| 阶段 | 工作与交付 | 完成条件 |
| --- | --- | --- |
| P0：本轮 | 写规格；复制文档、harness、工具、官方 driver、case、材料与必要配置；记录来源清单 | 复制内容哈希一致，批准文档保留，源仓库未改动；明确基线仍含旧命令/耦合 |
| P1：独立构建 | 更新 module/import；拆分 SDK 依赖测试；收敛 harness；固定工具链与依赖 | 工具独立编译；默认测试依赖无 Go SDK；原有断言有明确归属 |
| P2：三命令 | record 复用录制；官方 replay 收敛为 verify；提取 mock；移除公开 simulate/corpus/protocol | 对外只有三个业务入口；离线专项测试、race、vet 通过 |
| P3：材料与 mock | 构建内置集合；实现查询/准备/启动/收尾；保留外部材料 | 环境、回执、哈希和清理验收通过，源码目录外消费不需录制目录 |
| P4：消费者切换 | SDK simulate 调用外部 mock；联调 MASS；完成独立工具发布与版本 pin | SDK/MASS 业务与协议分别验收，完整覆盖缺口保留，再移除 SDK 旧维护副本 |

### 来源与批准

源仓库：`zoumo/agent-sdk-go`，导入时记录精确 revision 和逐文件哈希。
用户已批准三命令边界及 SDK/MASS 通过 mock 进程接入的测试位置，本规格不再重复询问。
设计目标见 [已批准设计](design.md)；复制清单见 [导入基线](import-baseline.json)。

### P0 基线检查结果

- 导入 1,278 个已跟踪文件，35,860,852 字节；逐文件 SHA-256 和可执行模式一致。
  根 Agent 指引另增加已批准边界与 tracker 配置，派生哈希单独登记；所有录制原字节不变。
- 复制基线的旧 module 下，工具生产二进制构建通过，版本为 `0.1.0-dev`。
- 六个协议包的离线 Go 测试通过；此结果不代表工具重构或 SDK 仿真完成。
- 目录检查确认 8 个 story、36 个 case、两套完整 provider binding、72 个固定索引项，
  driver 文档表与 catalog 一致。
- 原完整 story-evidence 检查未通过：文档扫描依赖 SDK 根 CHANGELOG，复制后该文件不存在。
  SDK 专属文档扫描与旧 harness 链接将在 P1 收敛；不复制整个 SDK 来伪造独立门禁通过。
- 未运行工具全量测试：其中仍有 Go SDK fixture/driver 耦合，按 P1 拆分归属。
  未执行 record、真实 Agent、官方真实材料 verify、SDK simulate 或 MASS E2E。
- 源 SDK 仍为导入 revision，工作区干净；未修改、提交或推送源仓库。

### 规格发布

用户已明确配置 GitHub 为 issue tracker；本项目使用 `zoumo/agent-sdk-testify` 的 GitHub Issues，
规格标签为 `ready-for-agent`。发布时使用此规格正文，完成后读取正文与标签确认。

### 已确认的 ticket 依赖图

父规格：[GitHub #1](https://github.com/zoumo/agent-sdk-testify/issues/1)。用户随后调用 implement-spec 确认执行；子任务已发布并建立原生阻塞关系。

| Ticket | 交付 | Blocked by |
| --- | --- | --- |
| [T1 / #2](https://github.com/zoumo/agent-sdk-testify/issues/2) | 独立构建与测试归属整理 | 无 |
| [T2 / #3](https://github.com/zoumo/agent-sdk-testify/issues/3) | 外部录制可直接作为 mock 进程使用 | [T1 / #2](https://github.com/zoumo/agent-sdk-testify/issues/2) |
| [T3 / #4](https://github.com/zoumo/agent-sdk-testify/issues/4) | record 通过官方 driver 完成录制 | [T1 / #2](https://github.com/zoumo/agent-sdk-testify/issues/2) |
| [T4 / #5](https://github.com/zoumo/agent-sdk-testify/issues/5) | verify 官方验证与材料采用 | T2, T3 |
| [T5 / #6](https://github.com/zoumo/agent-sdk-testify/issues/6) | 内置材料可查询并直接 mock | [T4 / #5](https://github.com/zoumo/agent-sdk-testify/issues/5) |
| [T6 / #7](https://github.com/zoumo/agent-sdk-testify/issues/7) | mock 环境准备与多连接收尾 | [T2 / #3](https://github.com/zoumo/agent-sdk-testify/issues/3) |
| [T7 / #8](https://github.com/zoumo/agent-sdk-testify/issues/8) | agent-sdk-go simulate 消费独立 mock | T5, T6 |
| [T8 / #9](https://github.com/zoumo/agent-sdk-testify/issues/9) | 三命令收敛与独立交付验收 | [T7 / #8](https://github.com/zoumo/agent-sdk-testify/issues/8) |

### 实施启动授权

用户在确认拆分后调用 implement-spec，授权按任务图使用独立 worktree、子代理、TDD、
本地提交与集成分支实施。集成分支为 `codex-testify-integration`，审查基线为 `867a671`。
首次复制阶段的不提交约束不再阻止技能要求的本地实现提交；推送、发布版本及真实录制仍未执行。
父规格保持开放，不因某个子任务完成而关闭；工作仅在实际验收完成后记为完成。
