# agent-sdk-testify

面向多语言 SDK 的 case、录制和 Agent mock 工具，对外只提供三个业务命令：

- `record`：按本仓库 case 录制真实 Agent。
- `verify`：用官方 SDK 对录制做离线验证。
- `mock`：使用录制充当 Agent，供消费者自己的测试连接。

本仓库维护 case、官方 driver、录制及材料库；`simulate`、Go driver 和 SDK 验收编排
留在 `agent-sdk-go`。SDK 与 MASS 直接消费本仓库的 mock。

设计已批准，已从 agent-sdk-go 复制文档、harness、代码、case 和已跟踪录制作为迁移基线。
工具现使用独立 module，源码位于仓库根目录，协议实现在根 `protocol/`。
当前 CLI 仍为待收敛的复制实现，三命令边界尚未实现。
[旧 CLI 参考](docs/legacy-cli.md)用于迁移期间，不代表最终公开接口。

- [mock 环境、运行描述与收尾接口](docs/mock-run.md)
- [已批准设计](docs/design.md)
- [实施规格与阶段规划](docs/implementation-plan.md)
- [导入来源与文件哈希](docs/import-baseline.json)
- [目标架构图（可缩放 HTML）](.archify/architecture-three-commands-20261102-181131/extraction.html)

## 独立构建与离线测试

使用 mise.toml 固定的 Go 1.26.9，不需要 Go SDK checkout、Go driver 或官方 SDK。
默认 Go 测试会构建自有工具进程；也可显式提供绝对路径 `AGENT_SDK_TESTIFY_BIN` 检查已有产物。

```sh
mise exec -- go build -o .artifacts/agent-sdk-testify .
mise exec -- go test ./...
mise exec -- go test -race ./...
mise exec -- go vet ./...
mise exec -- python3 -m unittest discover -s scripts -p 'test_*.py'
```

脚本测试和 wheel 构建需要 Python 3.11+、pip 和已安装的 setuptools>=68；不会安装官方 SDK。
`bash scripts/simulation.sh build` 仅构建本工具及官方 driver wheel。
旧 SDK simulate/gate 接入须由 SDK 提供 driver，不在本仓库构建。
SDK 专属断言原件与 T7 恢复要求见[待迁移材料](migration/agent-sdk-go/README.md)。
历史导入清单保持原路径与哈希，不随目录迁移改写；当前代码身份由 Git revision 确定。
常规检查全程离线，不执行真实 Agent 或新录制，也不构成 SDK 仿真通过的证据。
