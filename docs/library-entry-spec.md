# 可导入 Go 库入口规格

## Problem Statement

当前工具的主要使用方式仍是二进制入口。Go 使用方即使只需要 prepare、record、mock、
export 的 fixture 与协议能力，也必须先构建或安装 `agent-sdk-testify` 可执行文件，
再通过子进程和 argv 调用。现有库入口覆盖了一部分进程内能力，但还没有覆盖完整的
进程内 mock 生命周期。

现有库入口虽然已经可以调用 `Prepare`、`Open`、`Export` 和 `Fixture.MockAgent`，
但 `MockAgent` 只接受 `*os.File` 且是同步调用：调用方无法在回放完成前获知当前
execution 的 overlay、env 和 connection 身份，也无法在服务端脚本消费完毕时半关闭
SDK 输入；若同步等待调用返回，EOF 检查又依赖调用方先半关闭，形成时序缺口。这条
路径不能直接作为 `agent-sdk-go` 等消费者的零进程接入方案。

## Solution

根目录作为可导入 Go 库 `github.com/zoumo/agent-sdk-testify`，包名 `agenttestify`，
提供 prepare、open、export、record 以及两类 mock 入口：

- `Fixture.MockAgent(ctx, options, Streams)` 保留为 CLI/录制薄适配，继续使用
  `*os.File`、CLI descriptor 和进程接口。
- `Fixture.StartMockAgent(ctx, options, StartMockAgentStreams)` 提供进程内库入口，
  返回 `ExecutionStart`、完整消费事件和最终 `ExecutionReport`，不要求安装或启动
  `agent-sdk-testify` 可执行文件。

两种入口共享同一 fixture 目录、schema、execution 事务、严格回放、receipt 和材料
合同；official record/verify 仍只由 CLI 提供。入口职责以
[工具设计](design.md#入口与职责)为准。

## User Stories

1. 作为 Go 使用方，我希望直接导入模块路径使用工具能力，以便不需要安装二进制。
2. 作为 Go 使用方，我希望在进程内 prepare fixture，以便把它嵌入自己的测试流程。
3. 作为 Go 使用方，我希望库调用使用类型化参数和返回错误，以便不解析 argv 或进程输出。
4. 作为 Go 使用方，我希望库入口与 CLI 执行完全相同的校验和事务，以便两种入口结果一致。
5. 作为使用方，我希望库 prepare 的 fixture 与 CLI fixture 使用相同布局，以便共用现有检查工具。
6. 作为使用方，我希望在进程内 export current 材料，以便在其他 fixture 或进程中复用。
7. 作为 SDK 使用方，我希望在启动 SDK 客户端前获得本次 execution 的真实 workspace、
   protocol cwd、env 和 connection 身份，以便进程内客户端使用正确的隔离环境。
8. 作为 SDK 使用方，我希望在服务端脚本消费完毕时收到事件并半关闭输入，以便回放
   能够读到客户端 EOF 而不发生同步死锁。
9. 作为 SDK 使用方，我希望正常完成、调用方 abort 和 transport failure 使用不同
   终态，以便不会把半关闭造成的本地写错误误报为协议传输失败。
10. 作为 SDK 使用方，我希望 mock 返回覆盖全部声明连接的 typed report，以便不解析
    `execution.json`、receipt 或 result notice。
11. 作为 SDK 使用方，我希望多连接 execution 能通过显式 execution id 加入，以便
    并发客户端不会靠“最新目录”猜测归属。
12. 作为使用方，我希望库 mock 不修改宿主进程工作目录或全局环境，以便并发测试互不干扰。
13. 作为库使用者，我希望 record 仍可使用调用方提供的进程流，以便不改变现有采集路径。
14. 作为库使用者，我希望 embedded fixture 使用真实模块身份而不是伪造可执行命令，
    以便 fixture 来源可审计且不能被误启动。
15. 作为维护者，我希望进程内 mock 和 CLI mock 共用严格回放与 execution finalize，
    以便不维护两套协议行为。
16. 作为维护者，我希望库测试在没有工具二进制和 `PATH` 发现的情况下通过，以便证明
    库能力自足。
17. 作为维护者，我希望旧 schema 4 fixture 被明确拒绝，以便不会把 module path 当作
    可执行命令解释。

## Implementation Decisions

### 模块与入口

- 保持单一 module `github.com/zoumo/agent-sdk-testify`；根目录 Go 文件构成公开库包
  `agenttestify`，CLI 入口位于 `cmd/agent-sdk-testify`。不新增嵌套 module、go.work
  或第二套发布路径。
- 公开 API 为 `Prepare`、`Open`、`Export`、`Fixture.RecordAgent`、
  `Fixture.MockAgent`、`Fixture.StartMockAgent` 和
  `Fixture.ExecutionReport(executionID)`。参数与结果使用公开类型；库调用返回
  error，不返回退出码，也不写全局日志。CLI 负责把 error 映射为退出码和 stderr 诊断。
- `RecordAgent` 的公开签名保持 `*os.File` 的 `Streams`。record 需要真实原生 Agent
  或远程 endpoint，本次不引入进程内 record transport。
- `MockAgent` 的公开签名保持 `MockAgentOptions` 和 `Streams`，内部仍走进程适配、
  CLI 的 `--result-fd`/`--progress-fd` 兼容路径；库调用可传 `-1` 关闭 descriptor。
  它不是进程内 SDK 的推荐入口。
- `StartMockAgent` 使用独立选项和流类型；CLI 不调用该入口，也不需要伪造文件描述符。
- descriptor 通知、progress pipe、`--result-fd`/`--progress-fd` 和 argv 解析只属于
  CLI 适配层。库通过返回的 `ExecutionReport` 获得 typed 状态，不解析这些 descriptor。

### 进程内流

库消费者使用以下流合同：

```go
type StartMockAgentStreams struct {
    Reader io.ReadCloser  // SDK -> fixture；fixture 读取
    Writer io.WriteCloser // fixture -> SDK；fixture 写入
    Stderr io.Writer      // 诊断；不承载协议帧
}
```

`Reader` 和 `Writer` 是调用方创建并转让给 mock 的 fixture 侧端点；SDK 持有相反方向的
端点。`StartMockAgent` 返回后，调用方不得复用、并发关闭或重新赋值这两个端点。

- `Reader.Close` 必须解除 fixture 正在进行的阻塞 `Read`；它由 `MockRun` 在正常结束
  或 abort 时关闭。
- `Writer.Close` 必须解除 SDK 正在进行的阻塞 `Read`；fixture 在正常完成时读取到
  客户端 EOF、完成最终检查后关闭 `Writer`，abort/transport failure 时立即关闭。
- 任一方向的关闭都必须可并发调用且幂等；否则 abort 不能保证有界结束，执行必须记录
  cleanup failure，不能报告成功。
- `Stderr` 只接收诊断，不参与协议、完成判定或取消。它由调用方拥有，mock 不关闭；
  写入失败或丢弃不得改变协议结论。调用方应提供不会无限阻塞的诊断 writer。
- `Reader`、`Writer` 和 `Stderr` 均不得为 nil；`Reader` 与 `Writer` 不得是同一个
  对象。违反这些条件的调用在领取 execution 前失败。

SDK 必须与 mock 并行消费 `Writer`；否则服务端帧的写回背压会阻塞 replay，
`Wait` 不能代替读取最终帧。SDK 读到 EOF 后仍应继续完成自身清理并调用
`Wait(ctx)`。

### 启动 handle

```go
func (f *Fixture) StartMockAgent(
    ctx context.Context,
    options MockAgentOptions,
    streams StartMockAgentStreams,
) (*MockRun, error)

type MockRun struct {
    Start ExecutionStart
}

func (r *MockRun) FullConsumption() <-chan struct{}
func (r *MockRun) Done() <-chan struct{}
func (r *MockRun) Wait(ctx context.Context) (*MockAgentResult, error)
func (r *MockRun) Abort() error
```

`StartMockAgent` 在以下步骤完成后才返回：

1. 校验 fixture、current material、连接声明、流和 options。
2. 分配新的 mock execution，或按显式 `ExecutionID` 加入同一 execution。
3. 创建或打开 execution overlay，claim 当前 connection。
4. 写入 `running` start result 和 execution descriptor 哈希。
5. 构造不可变的 `ExecutionStart` 快照。

返回前不执行任何协议读写；replay goroutine 只有在 start 快照可用后才会释放。
`Start` 是启动快照字段，不阻塞，且在 `Wait` 前后都有效。`StartMockAgent` 在领取
execution 后失败时仍返回非 nil `MockRun` 和 error，调用方可用 `Wait(ctx)` 读取最终
失败证据；领取前的 preflight 失败返回 nil 和 error。`Start` 中的 map/slice 是只读
副本，调用方修改它们不会改变 execution 或 mock 使用的环境。

本设计不使用 `OnStarted` 或 `OnFullConsumption` callback。callback 在 replay
goroutine 上同步执行；如果它同步启动 SDK、等待 `Wait` 或调用整个 `Close`，就会
造成死锁或隐式重入。handle + 有界事件 channel 把控制权交还给调用方：

- 启动信息由 `StartMockAgent` 同步返回，调用方先得到 `ExecutionStart`，再启动 SDK。
- `FullConsumption()` 返回容量为 1 的 `<-chan struct{}`。到达完整消费点时发送
  一个 token 并关闭 channel；生产者不等待消费者，消费者只做半关闭。
- 若完整消费前失败，`FullConsumption` 不发送 token、也不关闭；`Done()` 关闭，
  `Wait(ctx)` 返回失败结果。消费者必须同时等待 `FullConsumption` 和 `Done`，不能
  把“channel 未触发”当成 hang，也不能把关闭当成成功。
- `Done()` 在本连接的 replay goroutine、receipt 写入和该 run 的 finalize 完成后
  关闭；多连接 execution 仍可继续 `open`。`Wait(ctx)` 等待同一终态并返回 typed
  receipt。任何方向提前 EOF 都不能替代 `Wait`。

`FullConsumption()` 和 `Done()` 返回两个独立 channel，各自只创建一次。前者只在
完整消费成功时发送并关闭，完整消费前失败或 abort 时保持未关闭；后者在任一终态
关闭。调用方可以在多个 goroutine 中安全地只读等待。

`Wait(ctx)` 是汇聚点，不是独立的后台运行：ctx 在终态前取消时，它请求内部 abort、
关闭 fixture 侧流，并继续等待 replay/finalize goroutine 结束，然后返回带
`canceled` failure class 的结果和 ctx error；不会在 goroutine 未收尾时提前返回。
`StartMockAgent` 接收的 ctx 持续约束 execution 生命周期；取消它同样触发内部 abort，
即使调用方尚未调用 `Wait`。若调用方在启动 SDK 前就取消或显式 `Abort`，mock 不会
触发 `FullConsumption`，`Done`/`Wait` 仍必须结束，不能伪造客户端 EOF 或成功。
`Wait` 可重复或并发调用，返回同一持久终态的事实；每次返回的 error 可以重新包装，
但不能改变摘要、状态或失败类别。execution 已到终态后，后到的 ctx 取消不再覆盖
已持久化的结论。

`MockAgentOptions` 在既有字段上增加显式 execution 加入：

```go
type MockAgentOptions struct {
    Connection  string
    ExecutionID string // 空值分配新 execution；非空值加入同一 execution
    Timeout     time.Duration
    NativeArgs  []string
}
```

`ExecutionID` 只由 `StartMockAgent` 消费；旧 `MockAgent` 进程适配路径遇到非空值时
返回配置错误，不把显式 join 和 CLI 的隐式 pending join 混用。

空 `ExecutionID` 不扫描“最新”或“未完成” execution 来自动加入；fixture 已有打开的
mock execution 时返回 busy。非空 `ExecutionID` 必须引用同一 fixture、同一 current
material、同一 timeout 和同一 native 声明，并只能 claim 尚未启动的声明连接。
join 失败在领取连接前返回，不创建空 execution。

### ExecutionStart

```go
type MockTransport string

const (
    MockTransportInProcess MockTransport = "in-process"
    MockTransportProcess   MockTransport = "process"
)

type ExecutionStart struct {
    FixtureID       string
    ExecutionID     string
    ConnectionID    string
    Material        Material
    ExecutionSHA256 string
    ExecutionPath   string

    Workspace       string
    ProtocolCwd     string
    Home            string
    Config          string
    Tmp             string
    HistoryFiles    map[string]string
    Env             map[string]string
    NativeArgs      []string
    Transport       MockTransport
}
```

`Workspace`、`ProtocolCwd`、`Home`、`Config`、`Tmp`、`HistoryFiles` 和 `Env`
必须来自本次 execution overlay，不能取自 `Prepare` 返回的 route template。每次
execution 的可写状态彼此隔离；调用方不得通过修改 `Fixture.Routes` 伪造本次启动环境。

`Env` 是 fixture 基线、execution overlay 与私有运行环境合并后的有效环境。调用方必须
把它作为 SDK transport 的完整替换环境传入，不修改 `os.Environ`，也不在全局
`os.Setenv`。敏感值只存在于内存合同，不写入 material、receipt、report 或日志。

`NativeArgs` 是本次连接的原生声明来源，不是进程 argv。进程内模式下 SDK 必须把受支持
的参数映射为自身 typed options；未知、重复或不支持的参数必须在打开 SDK 前失败，
不能静默忽略，也不能交给 `exec.Command`。

### Provider 映射

`agent-sdk-go` 的进程内 transport 必须按 provider 使用不同字段：

| Provider | SDK 入口 | cwd 映射 | 其他要求 |
| --- | --- | --- | --- |
| Codex | `codex.NewStreams` | `Dir = ExecutionStart.ProtocolCwd` | 仅逻辑协议 cwd；禁止 `os.Chdir`；不得伪造进程 `Dir` |
| Claude | `claude.OpenStreams` | `WorkDir = ExecutionStart.Workspace` | 原生 cwd 和 discovery 都使用该 workspace |

两者都使用同一个 `ExecutionStart.Env` 作为完整替换环境。Claude 的
`CLAUDE_CONFIG_DIR`、历史查询的 config/project 身份分别来自 `Env`、`Config` 和
`Workspace`；Codex 的 `CODEX_HOME` 来自 `Env` 或 `Config`。即使某 provider 当前
的 `ProtocolCwd` 与 `Workspace` 恰好相同，也必须分别读取，不能把两者合并成一个
可变字段。

### 完整消费与 SDK 半关闭

replay 在以下顺序到达完整消费点：

```text
所有服务端帧已发送/匹配
→ pending request 检查通过
→ 必要握手 barrier 通过
→ context 检查通过
→ 发送 FullConsumption 事件
→ 检查额外客户端字节
→ 等待客户端 EOF
→ 关闭 Writer，完成清理和 report
```

`FullConsumption` 事件只表示脚本化服务端帧和已到达请求已经满足，不表示 EOF、
cleanup 或 execution 成功。事件必须发生在额外字节/EOF 检查之前，否则调用方没有机会
半关闭输入；也不得早于 pending request 和握手检查，否则会把尚需响应的请求误判为完成。

`agent-sdk-go` 侧提供独立的 `CloseInput() error`：

- 第一次调用标记独立的 `inputClosed` 状态，只半关闭 SDK -> fixture 的 writer；
  不关闭 SDK 的 reader/stderr，不调用整个 `Client.Close`/`Session.Close`，不等待
  `writeGate`、ping、history 或 `MockRun.Wait`。
- 调用必须非阻塞且幂等。重复调用返回稳定的 `ErrInputClosed`，不产生第二次关闭。
- 半关闭之后在途写可能失败；这些错误必须映射为 `inputClosed`/`ErrInputClosed`，
  不能归类为 transport failure。fixture 只应观察到预期的客户端 EOF。
- 只有 `Wait` 的返回值是 testify peer 的权威完成结果。SDK 先读到 writer EOF 时
  不能立即判 pass 或 fail；必须继续完成自身清理并读取 `Wait`/`ExecutionReport`。

本连接正常完成要求：`FullConsumption` 到达、`CloseInput` 产生客户端 EOF、没有额外
客户端字节、本连接帧严格匹配、Writer 已关闭、清理完成。execution 整体 `passed`
还要求所有声明连接分别满足各自合同；任一条件不满足即失败。

### 终态与失败分类

`Wait` 返回的 result 和持久 execution index 共同决定终态。至少区分：

```go
type FailureClass string

const (
    FailureClassCanceled     FailureClass = "canceled"
    FailureClassAborted      FailureClass = "aborted"
    FailureClassTransport    FailureClass = "transport"
    FailureClassStrictReplay FailureClass = "strict-replay"
    FailureClassTimeout      FailureClass = "timeout"
    FailureClassCleanup      FailureClass = "cleanup"
    FailureClassInternal     FailureClass = "internal"
)
```

- **passed**：正常完成，所有声明连接和本连接完整消费、EOF、清理均成立。
- **canceled**：调用方 context 取消；不执行普通成功 finalize。
- **aborted**：调用方显式 `Abort`；关闭 Reader/Writer 以解除阻塞，不伪造 EOF。
- **transport**：流读写、断管、短写或意外 I/O 错误；立即失败并保留已知进度。
- **strict-replay**：请求顺序、内容、关联、额外字节或帧匹配失败。
- **timeout**：执行或 replay 超时。
- **cleanup**：协议结束但资源关闭、receipt 或 index finalize 未完成。
- **configuration**：fixture、material、connection、stream 或 options 前置条件不成立；
  在领取 execution 前返回，不产生成功结果。

`FailureClass` 和 `Error` 分别保存机器可读分类和诊断。`FullConsumption=true`
不能覆盖 transport、strict replay、timeout 或 cleanup 失败；`ClientEOF=false`
也不能由 abort 伪造成正常完成。

`Abort` 幂等，可和 `Wait` 并发调用；它取消执行并关闭 fixture 侧端点，但不生成独立
成功结果。`Wait` 仍是唯一最终报告来源。若流实现不满足“Close 解除阻塞”，`Abort`
只能返回有界失败并记录 cleanup failure。

### ExecutionReport

`ActionResult` 保留当前连接的部分事实；`MockAgentResult` 额外包含完整的
`Execution *ExecutionReport`：

```go
type ExecutionReport struct {
    FixtureID       string
    ExecutionID     string
    Status          ExecutionStatus
    Complete        bool
    Material        Material
    ExecutionSHA256 string
    ExecutionPath   string
    Connections     []ConnectionReport
}

type ConnectionReport struct {
    ID     string
    Status ConnectionStatus

    Transport       MockTransport
    FullConsumption bool
    ClientEOF       bool
    NativeLaunched  bool
    CleanupComplete bool
    ExitCode        *int
    Failure         FailureClass
    Error           string

    ReceiptPath        string
    ProcessReceiptPath string
    ResultPath         string
}
```

`Connections` 必须覆盖 execution descriptor 声明的全部 connection，而不只是本次
`Wait` 的 connection。进程内模式的 connection 使用 `Transport="in-process"`、
`NativeLaunched=false`、`ExitCode=nil`；不得伪造 PID、可执行文件或退出码。
其 `ProcessReceiptPath` 为空，不生成 process receipt；协议 receipt 仍保留完整消费、
客户端 EOF 和 cleanup 事实。进程适配模式保留真实 process receipt 的退出事实。

report 由 testify 自己读取并校验 execution descriptor、index、receipt 和 result
文件后生成；调用方不解析这些内部文件。descriptor 哈希、fixture id、material 哈希、
连接集合或终态 receipt 缺失/不一致时必须 fail closed：返回错误或 failed report，
不能把无法确认的事实当作 passed。

execution 仍为 `open` 时，尚未启动的连接返回 `pending`/`running` 是正常事实，不是
report 错误；已进入终态的连接仍必须有可校验的 receipt。连接集合必须与 descriptor
完全一致，不能只报告本次 `Wait` 的连接。

`Fixture.ExecutionReport(executionID)` 从持久事实重新生成同样的报告。`MockRun.Wait`
返回的是本连接 finalize 时的快照；多连接 execution 仍可能 `open`，调用方应在所有
连接结束后用 `ExecutionReport` 读取最终状态。

### 内嵌工具身份与 schema 5

`fixture.json`、`execution.json`、execution/material index 和 `current.json`
使用 schema 5。schema 4 不再读取；升级必须重新 prepare，不能把旧字段静默解释为新
身份。material/manifest 的自身格式不因 fixture schema 变化而改写。

工具身份使用判别式 union，而不是“module path 当 command”：

```go
type ToolIdentity struct {
    Source        ToolSource
    Version       string

    // executable
    Command       string
    Args          []string
    SHA256        string

    // embedded
    Module         string
    ModuleSum      string
    ModuleRevision string
    ModuleTime     string
    GoVersion      string
}
```

- `Source == executable`：`Command` 必须是绝对路径，`SHA256`、`Version` 非空；
  module 字段必须为空。
- `Source == embedded`：`Module` 必须是
  `github.com/zoumo/agent-sdk-testify`；`Command`、`Args` 和 `SHA256` 必须为空；
  不存在 launcher、argv 或可执行身份。`Version` 必须是精确 release、pseudo-version
  或带 `ModuleRevision` 的开发身份；不得只写未绑定 tag 的开发标签。
- `Open`/fixture/connection 校验接受 executable 或 embedded 判别式，但不得混合；
  embedded connection 不得引用 executable 字段，executable fixture 不得声明
  in-process connection。
- 版本从 `runtime/debug.ReadBuildInfo()` 读取。依赖模块优先使用其 `Version`/`Sum`；
  主模块使用精确 VCS revision（以及可用时的 commit time）形成可审计身份；无法得到
  精确身份或工作树标记为 modified 时，embedded `Prepare` 必须失败，不能生成可执行
  假身份。
- 非 main 包的 Go 测试二进制可能省略 package-under-test 的 module dependency。
  `PrepareOptions.EmbeddedIdentity` 因此提供显式回退：

  ```go
  type EmbeddedIdentity struct {
      Version   string
      Sum       string
      Revision  string
      Time      string
      GoVersion string
  }
  ```

  build info 已能证明本模块身份时，自证结果仍是权威值；显式输入不能覆盖它，且与
  build info 中已有字段冲突时必须失败。本模块从 build info 缺失时才使用回退，并要求
  build info 存在且 Go version 一致。`Version` 必须是精确版本，`Sum` 必须是精确
  `h1:` sum，`Revision` 必须是 40 或 64 位小写十六进制，`Time` 必须是 RFC 3339；
  pseudo-version 的时间戳和 revision 后缀还必须分别与 `Time`、`Revision` 一致。
  调用方应从 `go.mod` 和 `go.sum` 的 exact pin 生成这些测试常量，不得使用 replace、
  latest 或伪造 executable 身份。未提供该字段时，现有 `Prepare` 自证行为不变。

execution connection 同样使用判别式：

- `transport: "process"`：包含绝对 `command`、`args`、可选 `launcher`。
- `transport: "in-process"`：只包含 `id` 和 transport，不包含 `command`、`args`、
  `launcher` 或 `embedded` 布尔标记。

embedded fixture 的 CLI `agent mock` 和旧 `MockAgent(*os.File)` 进程适配路径都没有
可启动的 executable，必须明确拒绝；不得回退到当前进程或当前二进制。库
`StartMockAgent` 只接受 in-process connection。

## 公开结果与状态

record 返回 `RecordAgentResult`；mock 返回 `MockAgentResult`。两者继续嵌入
`ActionResult`，保留 `FixtureID`、`ExecutionID`、`ConnectionID`、
`ConnectionStatus`、`ExecutionStatus`、`ExecutionComplete`、材料身份和证据路径。
`ConnectionStatus` 只描述本次调用领取的连接；`ExecutionStatus` 汇总同一 execution
的全部声明连接；`ExecutionComplete` 只在 execution 已终态时为 true。

```go
run, err := fixture.StartMockAgent(ctx, agenttestify.MockAgentOptions{
    Connection: "main",
    NativeArgs: []string{"app-server", "--listen", "stdio://"},
}, agenttestify.StartMockAgentStreams{
    Reader: sdkToFixture,
    Writer: fixtureToSDK,
    Stderr: stderr,
})
if err != nil {
    return err
}
start := run.Start
// 用 start.Workspace、start.ProtocolCwd、start.Env 构造进程内 SDK transport。
go func() {
    select {
    case <-run.FullConsumption():
        _ = sdk.CloseInput()
    case <-run.Done():
    }
}()
result, err := run.Wait(ctx)
if err != nil {
    return err
}
if !result.Execution.Complete {
    // 同一 execution 的其他声明连接仍未完成。
}
```

`MockAgentResult.Execution` 是完整 execution 报告；`ActionResult` 仅用于兼容现有
调用方。无法从持久事实生成 report 时，mock 返回错误或 failed report，不返回
`ExecutionComplete=true` 的推测结果。

## 消费者接入合同（agent-sdk-go）

`agent-sdk-go` 的 `internal/mocktest` 和 SDK simulation 必须迁移到库入口：

- 直接 import `github.com/zoumo/agent-sdk-testify`，不再依赖
  `AGENT_SDK_TESTIFY_BIN`、`exec.CommandContext` 或 `session.json` schema 3。
- `Prepare` 不再启动 CLI；`Open` 读取 schema 5 fixture；多连接用显式
  `ExecutionID` 加入；不扫描目录或猜测最新 execution。
- 进程内客户端使用 `StartMockAgent`、`ExecutionStart`、`FullConsumption`、
  `CloseInput` 和 `ExecutionReport`；不解析 `execution.json`、receipt 或 result
  notice。
- Codex 和 Claude 的 transport 构造按上文 provider 映射；`Env` 必须替换而不是
  继承宿主环境，`Workspace`/`ProtocolCwd` 必须来自 `ExecutionStart`。
- `CloseInput` 维护独立的 `inputClosed`/`ErrInputClosed` 语义；半关闭导致的在途
  写错误不能升级为 transport failure，`Wait` 的结果不能被 reader EOF 抢先覆盖。
- SDK 更新对 testify 的版本 pin 时，必须指向包含 schema 5 和
  `StartMockAgent` 的精确 commit；旧 pin 与 schema 4 不兼容，不能混合运行。

## Testing Decisions

纵向验收接缝是公开 Go 库入口和 installed CLI 的消费者接口。库外部测试文件位于
根目录，包名 `agenttestify_test`，只导入公开模块路径，不导入 `internal`，不调用
CLI，并确保测试环境没有 `agent-sdk-testify` 可执行文件或可从 PATH 解析的工具。

库测试至少覆盖：

- 用 `io.Pipe` 或等价的可关闭流完成 prepare → `StartMockAgent` → SDK 半关闭 →
  `Wait` 的完整进程内回放；测试中不存在工具二进制。
- `ExecutionStart` 来自实际 execution overlay，包含当前 workspace、protocol cwd、
  env 和 connection；宿主进程 cwd 与全局环境不变。
- `FullConsumption` 在 pending request/握手之后、额外字节/EOF 之前到达；提前半关闭
  或重复 `CloseInput` 使用 `ErrInputClosed`，不会被报告为 transport failure。
- 正常完成、context cancel、显式 abort、transport failure、strict replay failure、
  timeout 和 cleanup failure 分别产生不同 failure class；abort 不伪造 EOF。
- 多连接通过显式 `ExecutionID` 加入；`ExecutionReport` 覆盖全部声明连接，进程内
  连接不包含伪造的 PID、executable 或退出码。
- schema 4 fixture 被拒绝，embedded identity 不含 module-path-as-command；library
  fixture 的 CLI process mock 被明确拒绝。

CLI 继续使用 installed-CLI conformance 测试，覆盖同一命令行为、退出码和
descriptor；不为库复制整套 CLI 用例。`MockAgent(*os.File)` 的兼容路径继续验证
`--result-fd`、`--progress-fd`、进程退出和现有 receipt；库测试不依赖这些 descriptor。

## Out of Scope

- 不为其他语言提供库绑定；非 Go 使用方继续使用 CLI。
- 不把 official record/verify 暴露为库 API，不改造 driver 进程边界。
- 不公开 protocol、材料和 raw frame 类型。
- 不在本次改造中实现进程内 record；record 仍使用真实 Agent/远程 endpoint。
- 不保证 library fixture 与 CLI fixture 可以交叉执行 action；schema 4 直接拒绝。
- 不改变 case 身份、required 集合、材料入仓政策或严格回放。
- 不新增真实录制、不启动真实 Agent 验证、不执行网络验证。
