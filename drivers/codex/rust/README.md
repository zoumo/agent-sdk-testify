# Codex Rust 候选入口

候选实现 37 个 Codex case，包括输入、工具、审批、配置、上下文注入、子会话、历史和
连接关闭。使用 Rust 1.99.1 和锁定的
`codex-app-server-remote-client` 0.0.0。解包固定提交后，`stdio.patch` 为上游
client 增加子进程管道接入。上游 client 管理握手、请求关联、事件、服务端请求和
关闭。driver 启动并等待 Testify 提供的 launcher。

准备 Rust 1.99.1、C/C++ 构建工具、`curl`、`tar`、`shasum` 和 `patch`。
在仓库根运行以下命令，在独立前缀安装候选入口。构建目录必须是新目录。

```sh
drivers/codex/rust/install.sh /tmp/testify-rust-build /tmp/testify-rust-install
```

首次构建下载固定源码和 Cargo 依赖。已有缓存时，通过 `CODEX_SOURCE_ARCHIVE`
传入固定源码归档的绝对路径，并设置 `CARGO_NET_OFFLINE=true`。

019 的真实录制需要 PATH 上的 `python3` 运行 Testify 提供的合成 MCP gate。
Testify 在单次录制 overlay 安装 gate，Rust 从根 Turn 的成功终态释放它。离线回放不启动 gate。

在临时工作目录对 Rust 形状的合成材料运行公开验证。Testify 从 `PATH` 选择候选入口，
并保留实际二进制和 SDK 身份。

```sh
export PATH="/tmp/testify-rust-install/bin:$PATH"
agent-sdk-testify prepare --recording /absolute/synthetic-rust-material \
	--fixture-dir /tmp/testify-rust-001
agent-sdk-testify official verify --fixture-dir /tmp/testify-rust-001 --timeout 45s
```

已采用材料保存了 Python client 的 `initialize` 帧。Rust client 的握手字段不同，
对这些材料的严格回放在第一帧失败。候选以 case 投影和运行时环境执行业务请求，
服务端审批通过原生请求 ID 应答。真实录制和已采用材料的迁移仍待验证。
