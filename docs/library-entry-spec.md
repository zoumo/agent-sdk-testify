# 可导入 Go 库入口规格

## Problem Statement

当前工具只有二进制入口。Go 使用方即使只需要 prepare、record、mock、export 的
会话与协议能力，也必须先构建或安装 `agent-sdk-testify` 可执行文件，再通过子进程
和 argv 调用。使用方无法在自己的进程内准备会话、回放协议或录制交互，工具身份、
stdio 和运行目录也被二进制入口独占。

## Solution

根目录作为可导入 Go 库 `github.com/zoumo/agent-sdk-testify`，包名 `agenttestify`，
提供 prepare、open、export、record、mock 的类型化入口；CLI 迁入
`cmd/agent-sdk-testify`。两者只是同一应用核心的薄适配器，共用参数校验、会话
生命周期、执行事务和材料规则，不维护第二套机制。CLI 行为、持久数据合同和严格
回放保持不变，official record/verify 仍只由 CLI 提供。

## User Stories

1. 作为 Go 使用方，我希望直接导入模块路径使用工具能力，以便不需要安装二进制。
2. 作为 Go 使用方，我希望在进程内 prepare 会话，以便把它嵌入自己的测试流程。
3. 作为 Go 使用方，我希望库调用使用类型化参数和返回错误，以便不解析 argv 或进程输出。
4. 作为 Go 使用方，我希望库入口与 CLI 执行完全相同的校验和事务，以便两种入口结果一致。
5. 作为使用方，我希望库 prepare 的会话与 CLI 会话使用相同布局，以便共用现有检查工具。
6. 作为使用方，我希望在进程内 export current 材料，以便在其他会话或进程中复用。
7. 作为使用方，我希望库 mock 通过我提供的 stdio 回放，以便驱动自己的 SDK 客户端。
8. 作为使用方，我希望库 mock 不修改宿主进程工作目录，以便并发测试互不干扰。
9. 作为使用方，我希望库 record 启动原生 Agent 时不依赖工具二进制，以便最小化部署。
10. 作为使用方，我希望库 record 的 current 切换、失败证据和结果回执与 CLI 相同，以便录制事实可比较。
11. 作为使用方，我希望 prepare 在库模式下记录可识别的工具身份，以便会话来源仍可审计。
12. 作为使用方，我希望库会话保留嵌入源标记，以便区分二进制产生的会话。
13. 作为使用方，我希望库不要求安装 CLI，也不在 PATH 中查找工具，以便完全离线使用。
14. 作为使用方，我希望库错误直接返回而不是退出进程，以便自行决定失败处理。
15. 作为使用方，我希望库 API 不暴露内部协议帧、匹配器或材料格式，以便依赖稳定边界。
16. 作为 CLI 用户，我希望现有命令、参数、退出码和输出保持兼容，以便升级不破坏脚本。
17. 作为 CLI 用户，我希望 `go build ./cmd/agent-sdk-testify` 构建二进制，以便路径明确。
18. 作为维护者，我希望根目录不再是可执行包，以便导入路径不会指向 main 包。
19. 作为维护者，我希望 prepare、record、mock、export 只有一个应用实现，以便不维护双份逻辑。
20. 作为维护者，我希望 CLI 只负责 argv、stdio、descriptor 和退出码，以便适配层保持单薄。
21. 作为维护者，我希望库入口使用最高层接缝验收，以便不依赖内部实现细节。
22. 作为维护者，我希望外部黑盒测试使用 `agenttestify_test` 包，以便验证真实公开 API。
23. 作为维护者，我希望库测试环境里没有工具二进制，以便证明能力不依赖 CLI。
24. 作为维护者，我希望 CLI conformance 测试继续通过进程接口验收，以便保留消费者视角。
25. 作为维护者，我希望 official 编排仍走 CLI 子进程，以便官方 driver 边界不改造。
26. 作为维护者，我希望库和 CLI 共用同一会话锁、执行事务和材料发布规则，以便并发行为一致。
27. 作为维护者，我希望布局迁移后同步构建脚本、发布脚本和文档，以便仓库入口不漂移。

## Implementation Decisions

- 保持单一 module `github.com/zoumo/agent-sdk-testify`；根目录 Go 文件构成公开库包
  `agenttestify`，CLI 入口位于 `cmd/agent-sdk-testify`。不新增嵌套 module、go.work
  或第二套发布路径。
- 新增内部应用核心，统一实现 prepare、record、mock、export 的参数校验、会话查找、
  execution 事务和结果交付。CLI 与公开库都调用该核心，均不直接编排 lifecycle、
  record、mock 或 corpus。
- 公开 API 为 prepare、open、export 与会话上的 record、mock。参数与结果使用公开
  类型；库调用返回 error，不返回退出码，也不写全局日志。CLI 负责把 error 映射为
  退出码和 stderr 诊断。
- 库 record/mock 接受显式 `Streams`，stdin、stdout、stderr 均为调用方提供；
  沿用 `*os.File`，不引入通用 I/O 抽象。CLI 传入自己的进程流。
- descriptor 通知、进度 pipe 和 argv 解析只属于 CLI 适配层。库通过返回结果或
  显式回调获得执行身份与状态，不复用文件 descriptor 协议。
- 回放与历史检查必须在指定工作目录内执行，不调用进程级 chdir；并发库调用不得
  改变宿主进程状态。
- prepare 支持嵌入工具身份：库调用不要求可执行文件路径，会话记录库模块身份、
  版本和嵌入源；CLI 调用继续记录实际可执行文件、版本与哈希。会话与连接校验接受
  两种身份形态，但不放宽 connection 与 route 的对应关系。
- 工具身份影响 route 连接模板与 launcher。嵌入会话不得声明无法执行的二进制
  launcher；库 mock 直接从调用方 stdio 回放，不依赖该 launcher。
- 库与 CLI 会话共享目录布局、schema、材料哈希与锁；v1 不保证库创建的会话能被
  CLI 执行 action，也不反向保证 CLI 会话能在库中执行，但材料可以互相导出导入。
- official record/verify、driver 进程边界和 Python wheel 保持 CLI 专属，不进入
  库 API，也不改造为库调用。
- 现有 agent record/mock 协议、材料字节、严格回放、current 切换和失败证据规则
  不变；库化不得弱化任何校验。
- 布局迁移同步 Makefile、构建/检查/包装/发布脚本、内置快照生成命令、测试构建
  路径及 README、工具设计、开发规范。

## Testing Decisions

最高验收接缝是公开 Go 库包。外部测试文件位于根目录，包名 `agenttestify_test`，
只导入公开模块路径，不导入 `internal`，不调用 CLI，并确保测试环境没有
`agent-sdk-testify` 可执行文件或可从 PATH 解析的工具，以证明库能力自足。

测试只断言外部可观察行为：会话产物、执行状态、材料身份与哈希、回放结果、错误
类别和宿主进程 cwd 不变。不针对私有函数或内部结构写断言。

CLI 继续使用现有 installed-CLI conformance 测试，覆盖同一命令行为、退出码和
descriptor；不为库复制整套 CLI 用例。application 核心的特殊边界可在其上层用
同一公开行为验证。

逐 ticket 只做必要的构建和静态检查，不运行完整测试；全部 ticket 实现后集中执行
开发规范要求的完整离线门禁并修复失败，既定验收覆盖保持不变。

## Out of Scope

- 不为其他语言提供库绑定；非 Go 使用方继续使用 CLI。
- 不把 official record/verify 暴露为库 API，不改造 driver 进程边界。
- 不公开 protocol、材料和 raw frame 类型。
- 不保证库会话与 CLI 会话可以交叉执行 action。
- 不改变 case 身份、required 集合、材料入仓政策或严格回放。
- 不新增真实录制、不启动真实 Agent 验证、不执行网络验证。

## Further Notes

Issue tracker 为仓库配置的 GitHub Issues，使用 ready-for-agent 标签。
任务按依赖顺序实施；每张 ticket 使用独立 chat 和独立分支，完成后合并回目标分支
`codex/library-entry`，不直接改动 main。主 chat 负责编排、核对合并，并在全部
ticket 实现后统一执行离线门禁与修复。

外部测试包名固定为 `agenttestify_test`，与库包 `agenttestify` 同目录。
