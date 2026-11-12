# agent-sdk-testify

面向多语言 SDK 的 case、录制和 Agent mock 工具。公开 CLI 入口为：

- `prepare`：按 case、consumer plan 或已有录制创建一个可复用隔离 fixture。
- `agent record`：SDK 启动的采集代理，转发真实 Agent 协议并发布录制材料。
- `agent mock`：SDK 启动的回放对端，用录制替代真实 Agent。
- `official record`：驱动官方 SDK 调用 agent record，保存录制业务事实。
- `official verify`：驱动官方 SDK 调用 agent mock，核验并保存验证事实。
- `export`：把 current recording 完整导出到 fixture 外，供另一 fixture 导入。

命令职责、execution 归属和 fixture 协调见[工具设计](docs/design.md)。

## Go 库入口

Go 使用方可以导入模块根包：

```go
import agenttestify "github.com/zoumo/agent-sdk-testify"

fixture, err := agenttestify.Prepare(agenttestify.PrepareOptions{
    FixtureDir: "/private/fixture",
    Plan:       "/private/plan.json",
})
```

根包 `agenttestify` 提供类型化的 `Prepare`、`Open`、`Export`，以及
`Fixture.RecordAgent`、`Fixture.MockAgent` 和进程内 `Fixture.StartMockAgent`，不要求
安装 CLI。进程内 SDK 使用 `StartMockAgent` 的可关闭双向流，在协议 I/O 前读取
`MockRun.Start`，通过 `FullConsumption` 半关闭输入，并从 `Wait` 取得权威结果；
`MockAgent` 保留 `*os.File` 进程适配。`official record`、`official verify` 和官方
driver 编排仍只由 CLI 提供；协议帧、严格匹配器和材料内部格式不属于库 API。v1 不保证
库 fixture 与 CLI fixture 可交叉执行 action，材料通过 export 和 prepare 交换。完整合同见
[可导入 Go 库入口规格](docs/library-entry-spec.md)。

非 main 包测试二进制的 build info 可能省略本 module dependency。此时可把 `go.mod`
和 `go.sum` 中 exact pin 的版本、sum、revision、commit time 与 Go version 作为
`PrepareOptions.EmbeddedIdentity` 传入；build info 能自证时仍以自证结果为准。

record 和 mock 返回连接状态、整个 execution 状态及终态标志。连接成功不等于 execution 完成：

```go
result, err := fixture.MockAgent(ctx, agenttestify.MockAgentOptions{
    NativeArgs: []string{"app-server", "--listen", "stdio://"},
},
    agenttestify.Streams{In: stdin, Out: stdout, Err: stderr})
if err != nil {
    return err
}
if result.ConnectionStatus == agenttestify.ConnectionStatusPassed &&
    !result.ExecutionComplete {
    // 同一 execution 仍有其他声明连接未完成。
}
```

本仓库维护 case、官方 driver、录制及材料库；`simulate`、Go driver 和 SDK 验收编排
留在 `agent-sdk-go`。SDK 与 MASS 直接消费本仓库的 mock。

工具使用独立 module，CLI 入口位于 `cmd/agent-sdk-testify`，命令实现按功能位于
`internal/`，协议位于
`internal/protocol/`。权威场景、采用材料和内置快照统一位于 `data/`，官方 driver
与 wheel 位于 `drivers/`。
不提供 simulate/replay/corpus/protocol/peer 业务入口。
旧顶层 record/mock/verify 与 internal-protocol（含 --internal-protocol）均被拒绝，不保留别名；远程桥接由 agent record 内部处理。
版本只由 Git tag 表示，源码不维护版本常量；CLI 从构建时的精确 tag 或 Go module build info 读取版本。

规范流程是 prepare、协议执行或官方编排。调用方在所有相关进程退出后删除 fixture。agent record 成功时原子替换
唯一 current recording，agent mock 和 official verify 消费 current。官方命令保存
业务事实并引用 agent execution，不创建自己的协议 execution。材料来源、环境隔离
和发布合同见[工具设计](docs/design.md)。

## 文档

| 主题 | 文档 |
| --- | --- |
| 工具边界、合同与材料链路 | [工具设计](docs/design.md) |
| 可导入库 API 与 v1 边界 | [可导入 Go 库入口规格](docs/library-entry-spec.md) |
| 模块、依赖、布局、编码与测试 | [开发规范](docs/development.md) |
| 8 个用户故事与 38 个 case | [共同验收](docs/cases.md) |
| 从 SDK 测试接入、运行与收尾 mock | [mock 使用指南](docs/using-mock.md) |
| 外部消费者自己录制并用 mock 回放 | [消费者录制](docs/consumer-recording.md) |
| 官方 driver、SDK 环境和已知限制 | [driver 说明](drivers/README.md#官方-sdk-环境) |
| 术语 | [GLOSSARY](GLOSSARY.md) |

## 独立构建与离线测试

使用 Go 1.26.10 和 golangci-lint 2.12.2，不需要 Go SDK checkout、Go driver 或官方 SDK。
CLI 也可以用 `go build ./cmd/agent-sdk-testify` 独立构建。
`make test` 运行根目录公开库测试和内部单元测试，不构建 CLI。

Go 1.26.10 的 linked worktree 构建请使用这些标准入口：先提交源码，入口会从精确 HEAD
创建本地独立构建 checkout，以保留 Go 的 VCS 身份。dirty 输入会被拒绝；失败时输出
保留的 checkout/日志位置，产物仍写入调用方目录。聚焦测试可运行
`bash scripts/with-build-checkout.sh go test -buildvcs=true -run 'TEST' .`。

构建、集成和完整流程分别运行：

```sh
make build
make test                 # 公开库与内部单元测试
make test-integration     # 多模块协作，必要的受控进程
make test-race            # 公开库、单元与集成的 race 检查
make test-e2e             # 构建一次 race CLI，运行完整流程
make test-build           # 生成器、产物模式和发布冒烟
make check                # 完整离线门禁
make vet
make lint
make fmt
```

`go test ./...` 会选择全部层，并非快速入口。直接运行 E2E 时，用
`bash scripts/with-agent-sdk-testify.sh --race go test -race ./tests/e2e/...`；
也可传绝对路径 `AGENT_SDK_TESTIFY_BIN` 复用产物，构建模式必须与测试一致。
测试包不自行构建 CLI。测试分层规则见[开发规范](docs/development.md#测试)。

脚本测试和 wheel 构建需要 Python 3.11+、pip 和已安装的 setuptools>=68；不会安装官方 SDK。
`make bundle` 仅构建本工具及官方 driver wheel。
SDK 自己构建 simulate/gate，通过显式 `AGENT_SDK_TESTIFY_BIN` 消费 mock；发布 pin 仍为 null。
常规检查全程离线，不执行真实 Agent 或新录制，也不构成 SDK 仿真通过的证据。

## 按 case 录制

按 [driver 环境说明](drivers/README.md#官方-sdk-环境)显式安装本仓库 wheel 和所选官方 SDK；Codex 使用固定官方源码及 `--no-deps`，
将该环境的 bin 目录放入 PATH。工具内部选择 `agent-sdk-testify-codex` 或
`agent-sdk-testify-claude`；不会下载、安装或改用其他 SDK 包。
安装需满足该官方 SDK 版本的 Python 与包源要求；wheel 本身安装成功不证明 SDK 依赖可用。

```sh
agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
  --fixture-dir /private/fixture
agent-sdk-testify official record --fixture-dir /private/fixture \
  --cmd /absolute/codex -- app-server --listen stdio://
agent-sdk-testify official verify --fixture-dir /private/fixture
agent-sdk-testify agent mock --fixture-dir /private/fixture
agent-sdk-testify export --fixture-dir /private/fixture --output /private/exported-recording
```

case 与 binding 随二进制嵌入，可离开源码目录使用；不需要 `--materials` 或 `--driver`。
配置和认证由工具从 agent type 标准配置位置发现并筛选，不整体复制个人 skills、插件或指令。
`--model`、`--initial-model`、`--history-seed` 等环境选项属于 prepare；
`--timeout` 等执行选项属于具体 action。所有 prepared-fixture 命令的
`--fixture-dir` 都指向同一个 fixture 目录；prepare 要求该目录尚不存在并负责创建，
后续命令读取该目录。`fixture.json` 是工具内部 descriptor，不是 CLI 参数。
失败与未触发尝试仍保留原始 plan/raw、业务状态、stderr、采集与清理回执；准备阶段失败保留诊断。
录制通过不等于官方验证、审核采用或发布通过，record 不自动执行这些后续步骤。

默认离线测试只以受控进程替换官方 driver 和 native Agent，不执行真实 SDK、模型或录制调用。

外部消费者可以 `prepare --plan`，再用 record 让自身 SDK 驱动真实 Agent。工具准备
隔离环境、workspace、配置和历史种子，消费者只提供真实 Agent 启动命令/参数并在 SDK
宿主应用公开 Go 运行时 route 或 CLI runtime route 快照；record 产物可在同一 fixture 中直接 mock。该路径不经过官方
driver，也不进入官方 case、验证、采用或内置快照；合同和用法见
[消费者录制](docs/consumer-recording.md)。

SDK 录制已有 WebSocket 服务时，启动同一个公开进程：

```sh
agent-sdk-testify agent record --fixture-dir /private/fixture \
  --url ws://127.0.0.1:8765/agent --token-file /private/runtime/bearer-token \
  --timeout 2m --result-fd 3
```

`--url` 与 `--cmd`、`--` 后的本地参数互斥。token 文件须为 runtime 内或 fixture 外的绝对路径、
普通私有文件（例如权限 `0600`），内容为 bearer token。endpoint 和 token 文件路径
不进入持久 execution 或材料。SDK 通过 stdin/stdout 交换 JSONL；可选的 `--result-fd`
沿用独立执行通知接口，调用方需先提供对应文件描述符。结束时关闭 stdin，工具只关闭
自己的连接；已有服务由调用方管理。

## 官方离线验证与采用

在已安装上述官方 driver 和 SDK 的环境中执行：

```sh
agent-sdk-testify prepare --agent codex \
  --recording /private/new-capture --fixture-dir /private/fixture
agent-sdk-testify official verify --fixture-dir /private/fixture --timeout 2m
```

agent type/case 从录制身份确定，只选择本工具的官方 driver；不接受 `--driver`、
`--driver-arg`、`--peer` 或 SDK 仿真 driver。不会启动真实 Agent 或自动安装依赖。
原件与已有 replay 证据保持不变；official verify 驱动官方 driver，由 driver 启动公开 agent mock。
业务报告、driver 退出与失败诊断保存在 `packages/<materialId>/verify/<verificationId>/`，协议与
连接退出回执由 mock execution 保存，`verification.json` 引用实际参与的 execution。
验证证据绑定原件、合同、工具及 driver 哈希，
记录实际安装的官方 SDK 包和版本；版本不兼容须报告失败。
业务通过仍须所有连接严格匹配、完整消费、EOF、退出及清理通过。

新录制经验证后可直接沿用内部资格、私有归档与原子采用流程：

```sh
agent-sdk-testify official verify --fixture-dir /private/fixture \
  --index /absolute/data/index.json \
  --scenarios /absolute/data/scenarios --archive /private/archives
```

`--index` 启用采用；`--scenarios` 默认取 index 的相邻 `../scenarios`。
资格要求沿用既有隔离、来源、官方业务与材料规则，不生成新的内容审核或扫描流程。
`admission-summary.json` 单独报告采用结果；采用失败保留验证证据和旧索引/材料。
SDK simulate 不参与采用判断。资格与采用成功不单独证明内容允许公开发布。

## 构建交付与消费者

`make release OUTPUT=/absolute/new-dist` 生成 Linux/macOS
amd64/arm64 独立归档、官方 driver wheel、manifest.json 和 SHA256SUMS。
先显式准备 Go module 与 Python 构建依赖；脚本不会下载依赖、推送或发布。
发布必须从带精确 Git tag 的提交构建；未打 tag 时构建仍显示 `devel`，但不允许生成 release。
版本、工具源码 revision、内置材料 revision/哈希和逐产物校验和分别记录。
运行 `make check` 执行离线完整门禁；CI 分开准备依赖后执行同一入口。

消费者按 [mock 使用指南](docs/using-mock.md) prepare、在 fixture 中
选择 mock、执行自身业务、关闭宿主并等待相关进程退出，最后自行删除 fixture。SDK/MASS 的业务成功与工具协议成功
分别判断。MASS 的 mass/2 Codex
握手与录制中的官方客户端不同时应失败，不得修改录制或放宽字段。
[driver 环境与已知限制](drivers/README.md#官方-sdk-环境)记录实际依赖和来源。
