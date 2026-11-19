# Go 库回放合同

导入 `github.com/zoumo/agent-sdk-testify`，包名 `agenttestify`。进程内回放使用
`Fixture.StartMockAgent`。公开签名、类型和字段见[根包源码](../library.go)，进程入口与官方
编排的职责见[工具设计](design.md#入口与职责)。

## 进程内流

调用方创建两条独立流。`StartMockAgent` 领取连接后，mock 接管 `Reader`（SDK 发往
mock）和 `Writer`（mock 发往 SDK），并负责关闭它们。调用方将另一侧的端点交给 SDK。
两条流的 `Close` 必须解除对应方向的阻塞 I/O，并支持并发和重复关闭。`Stderr` 由调用方保有，可以传
`io.Discard`。诊断写入须及时返回，以免阻塞清理。

## 启动 handle

1. 调用 `StartMockAgent`。新 execution 留空 `ExecutionID`。同一 execution 的其他声明连接
   用首次启动返回的 `ExecutionID` 显式加入，并沿用同一 fixture、current material、超时和
   对应连接的原生参数。已有打开的 execution 时，留空会返回 busy。
2. 启动 SDK 前读取 `run.Start`。用其中的实际 execution overlay 初始化 SDK。把 `Env` 作为
   完整替换环境，保持宿主进程的 cwd 和全局环境。Codex `NewStreams` 的 `Dir` 取
   `ProtocolCwd`，Claude `OpenStreams` 的 `WorkDir` 取 `Workspace`。把 `NativeArgs`
   映射为 SDK 的类型化选项，启动前拒绝未知、重复或不支持的参数。
3. 启动 SDK 并持续读取 mock 输出。在独立 goroutine 中同时等待 `FullConsumption()` 和
   `Done()`。收到完整消费信号后调用 SDK 的 `CloseInput()`，只关闭发往 mock 的写端，
   然后继续读取到 EOF。
   事件在服务端帧、待回复请求和握手检查完成后发出，先于额外客户端字节和 EOF 的检查。
   若提早失败，`FullConsumption()` 保持打开，`Done()` 在本连接收尾后关闭。SDK 的半关闭
   须能及时返回，半关闭后的本地写错误使用 `ErrInputClosed`。
4. 用 `Wait(ctx)` 判定本连接的终态。输出 EOF 或完整消费都只提供部分证据。终态前取消
   启动 context 或 `Wait` 的 context，或显式调用 `Abort()`，会请求停止并关闭 mock 端点。
   `Wait` 在 receipt 和收尾完成后才返回，可重复或并发调用。后到的取消保留已持久化的终态。
5. 关闭 SDK，在所有声明连接结算后用 `Fixture.ExecutionReport(executionID)` 读取多连接
   execution 的最终汇总。只有全部连接均严格匹配、完整消费、收到客户端 EOF 并完成清理时，
   execution 才通过。最后按 fixture 的所有权规则清理目录。
   [SDK 宿主的协调区间](design.md#sdk-宿主的运行时入口) · [fixture 所有权](design.md#fixture-目录所有权)

`StartMockAgent` 在领取前失败时返回 nil handle。若同时返回 handle 和错误，使用 `Wait`
读取已领取连接的失败证据。SDK 启动失败时调用 `Abort()`，再调用 `Wait` 收尾。

## ExecutionReport

`Wait` 的报告反映本连接结算时的 execution。此时其他连接可能仍在运行或尚未启动。
`Fixture.ExecutionReport` 从持久证据重新校验并汇总全部声明连接。`Complete` 表示整个
execution 已结算，通过还要求 `Status` 为 `ExecutionStatusPassed`。终态证据缺失、身份或
哈希不一致时按失败处理。调用方通过 Go 结果读取结论和证据路径。
[路径相关证据](design.md#路径相关证据)

## 内嵌工具身份与持久化合同

`Prepare` 优先从 Go build info 解析 Testify 的精确模块身份。调用方的非 main 测试二进制
省略该模块时，可用 `PrepareOptions.EmbeddedIdentity` 提供固定依赖的身份，值取自固定版本的
`go.mod`、`go.sum` 和对应提交。显式身份必须与已有 build info 一致。字段校验和用法见
[身份解析源码](../internal/version/identity.go)与[公开库测试](../library_test.go)。
内嵌身份用于进程内回放。可执行身份与 schema 兼容规则见[框架路径与录制身份](design.md#框架路径与录制身份)
和[schema 版本与兼容](design.md#schema-版本与兼容)。

公开验收覆盖完整消费与半关闭的顺序、取消与失败、显式加入和多连接报告，运行时无需工具二进制。
[进程内回放测试](../library_mock_inprocess_test.go) · [多连接测试](../library_startmock_join_test.go) · [报告校验测试](../library_execution_report_test.go)
