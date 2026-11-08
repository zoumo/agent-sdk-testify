# 可导入 Go 库入口规格

## Problem Statement

当前工具只有二进制入口。Go 使用方即使只需要 prepare、record、mock、export 的
fixture 与协议能力，也必须先构建或安装 `agent-sdk-testify` 可执行文件，再通过
子进程和 argv 调用。使用方无法在自己的进程内准备 fixture、回放协议或录制交互，
工具身份、stdio 和运行目录也被二进制入口独占。

## Solution

根目录作为可导入 Go 库 `github.com/zoumo/agent-sdk-testify`，包名 `agenttestify`，
提供 prepare、open、export 以及 `Fixture.RecordAgent`、`Fixture.MockAgent`
的类型化入口；CLI 迁入 `cmd/agent-sdk-testify`。入口职责以
[工具设计](design.md#入口与职责)为准。两种入口共享 fixture 目录、schema 和
持久数据合同；official record/verify 仍只由 CLI 提供。

## User Stories

1. 作为 Go 使用方，我希望直接导入模块路径使用工具能力，以便不需要安装二进制。
2. 作为 Go 使用方，我希望在进程内 prepare fixture，以便把它嵌入自己的测试流程。
3. 作为 Go 使用方，我希望库调用使用类型化参数和返回错误，以便不解析 argv 或进程输出。
4. 作为 Go 使用方，我希望库入口与 CLI 执行完全相同的校验和事务，以便两种入口结果一致。
5. 作为使用方，我希望库 prepare 的 fixture 与 CLI fixture 使用相同布局，以便共用现有检查工具。
6. 作为使用方，我希望在进程内 export current 材料，以便在其他 fixture 或进程中复用。
7. 作为使用方，我希望库 mock 通过我提供的 stdio 回放，以便驱动自己的 SDK 客户端。
8. 作为使用方，我希望库 mock 不修改宿主进程工作目录，以便并发测试互不干扰。
9. 作为使用方，我希望库 record 启动原生 Agent 时不依赖工具二进制，以便最小化部署。
10. 作为使用方，我希望库 record 的 current 切换、失败证据和结果回执与 CLI 相同，以便录制事实可比较。
11. 作为使用方，我希望 prepare 在库模式下记录可识别的工具身份，以便 fixture 来源仍可审计。
12. 作为使用方，我希望库 fixture 保留嵌入源标记，以便区分二进制产生的 fixture。
13. 作为使用方，我希望库不要求安装 CLI，也不在 PATH 中查找工具，以便完全离线使用。
14. 作为使用方，我希望库错误直接返回而不是退出进程，以便自行决定失败处理。
15. 作为使用方，我希望库 API 不暴露内部协议帧、匹配器或材料格式，以便依赖稳定边界。
16. 作为 CLI 用户，我希望命令只接受 `--fixture-dir`，以便旧 session 参数和描述符不会被误用。
17. 作为 CLI 用户，我希望 `go build ./cmd/agent-sdk-testify` 构建二进制，以便路径明确。
18. 作为维护者，我希望根目录不再是可执行包，以便导入路径不会指向 main 包。
19. 作为维护者，我希望 prepare、record、mock、export 只有一个应用实现，以便不维护双份逻辑。
20. 作为维护者，我希望 CLI 只负责 argv、stdio、descriptor 和退出码，以便适配层保持单薄。
21. 作为维护者，我希望库入口使用最高层接缝验收，以便不依赖内部实现细节。
22. 作为维护者，我希望外部黑盒测试使用 `agenttestify_test` 包，以便验证真实公开 API。
23. 作为维护者，我希望库测试环境里没有工具二进制，以便证明能力不依赖 CLI。
24. 作为维护者，我希望 CLI conformance 测试继续通过进程接口验收，以便保留消费者视角。
25. 作为维护者，我希望 official 编排仍走 CLI 子进程，以便官方 driver 边界不改造。
26. 作为维护者，我希望库和 CLI 共用同一 fixture 锁、执行事务和材料发布规则，以便并发行为一致。
27. 作为维护者，我希望布局和术语迁移后同步构建脚本、CLI、测试、发布脚本和文档，以便仓库入口不漂移。

## Implementation Decisions

- 保持单一 module `github.com/zoumo/agent-sdk-testify`；根目录 Go 文件构成公开库包
  `agenttestify`，CLI 入口位于 `cmd/agent-sdk-testify`。不新增嵌套 module、go.work
  或第二套发布路径。
- 新增内部应用核心，统一实现 prepare、record、mock、export 的参数校验、fixture 查找、
  execution 事务和结果交付。入口与内部依赖边界遵循
  [工具设计](design.md#入口与职责)。
- 公开 API 为 `Prepare`、`Open`、`Export` 与 `*Fixture` 上的 `RecordAgent`、`MockAgent`。
  参数与结果使用公开类型；库调用返回 error，不返回退出码，也不写全局日志。CLI
  负责把 error 映射为退出码和 stderr 诊断。
- 库 record/mock 分别接受 `RecordAgentOptions`、`MockAgentOptions` 和显式
  `Streams`；stdin、stdout、stderr 均为调用方提供，沿用 `*os.File`，不引入通用
  I/O 抽象。CLI 传入自己的进程流。
- descriptor 通知、进度 pipe 和 argv 解析只属于 CLI 适配层。库通过返回的 action
  结果获得执行身份与状态，不复用文件 descriptor 协议。
- 回放与历史检查从 fixture 和 execution overlay 的绝对路径定位文件，不依赖或
  改变进程工作目录；并发库调用不得改变宿主进程状态。
- prepare 支持嵌入工具身份：库调用不要求可执行文件路径，fixture 记录库模块身份、
  版本和嵌入源；CLI 调用继续记录实际可执行文件、版本与哈希。fixture 与连接校验接受
  两种身份形态，但不放宽 connection 与 route 的对应关系。
- 工具身份影响 route 连接模板与 launcher。嵌入 fixture 不得声明无法执行的二进制
  launcher；库 mock 直接从调用方 stdio 回放，不依赖该 launcher。
- 库与 CLI fixture 共享目录布局、schema、材料哈希与锁；v1 不保证库创建的 fixture
  能被 CLI 执行 action，也不反向保证 CLI fixture 能在库中执行，但材料可以互相
  导出导入。
- official record/verify、driver 进程边界和 Python wheel 保持 CLI 专属，不进入
  库 API，也不改造为库调用。
- 现有 agent record/mock 协议、材料字节、严格回放、current 切换和失败证据规则
  不变；库化不得弱化任何校验。
- 布局迁移同步 Makefile、构建/检查/包装/发布脚本、内置快照生成命令、测试构建
  路径及 README、工具设计、开发规范。

## 公开结果与状态

record 和 mock 返回 `RecordAgentResult` 或 `MockAgentResult`，两者都嵌入 `ActionResult`。
`ConnectionStatus` 只描述本次调用领取的连接；`ExecutionStatus` 汇总同一 execution
的全部声明连接；`ExecutionComplete` 仅在 execution 已到达终态 `passed` 或 `failed`
时为 true。连接成功不等于 execution 完成。状态以 execution index 的最终事实为准。
无法从 index 刷新状态时两个状态均为 `unknown`，调用方应结合返回错误和证据路径诊断。

```go
result, err := fixture.MockAgent(ctx, agenttestify.MockAgentOptions{
    Connection: "main",
    NativeArgs: []string{"app-server", "--listen", "stdio://"},
}, agenttestify.Streams{In: stdin, Out: stdout, Err: stderr})
if err != nil {
    return err
}
if result.ConnectionStatus == agenttestify.ConnectionStatusPassed &&
    !result.ExecutionComplete {
    // 同一 execution 的其他声明连接仍未完成。
}
```

`ActionResult` 还给出 `FixtureID`、execution descriptor、连接 receipt、进程 receipt
和持久 result 的路径；成功 record 和 mock 结果包含本次材料身份。路径均从绝对
fixture 或 execution 根解析。

mock 进程 receipt 的字段合同见[工具设计](design.md#agent-mock)。

## Testing Decisions

纵向验收接缝是公开 Go 库入口和 installed CLI 的消费者接口。库外部测试文件位于
根目录，包名 `agenttestify_test`，只导入公开模块路径，不导入 `internal`，不调用
CLI，并确保测试环境没有 `agent-sdk-testify` 可执行文件或可从 PATH 解析的工具，
以证明库能力自足。CLI 使用已构建二进制的 argv、stdio、退出码和 descriptor 验收。

公开库测试只断言外部可观察行为：fixture 产物、执行状态、材料身份与哈希、回放结果、
错误类别和宿主进程 cwd 不变。`application` 的私有 preflight 边界由同包单元测试
覆盖，纵向验收不依赖这些内部断言。

CLI 继续使用现有 installed-CLI conformance 测试，覆盖同一命令行为、退出码和
descriptor；不为库复制整套 CLI 用例。两种入口的 fixture 与材料合同通过同一
fixture 比较，application 核心的特殊边界在其上层用同一公开行为验证。

## Out of Scope

- 不为其他语言提供库绑定；非 Go 使用方继续使用 CLI。
- 不把 official record/verify 暴露为库 API，不改造 driver 进程边界。
- 不公开 protocol、材料和 raw frame 类型。
- 不保证库 fixture 与 CLI fixture 可以交叉执行 action。
- 不改变 case 身份、required 集合、材料入仓政策或严格回放。
- 不新增真实录制、不启动真实 Agent 验证、不执行网络验证。
