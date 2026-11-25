# Codex Rust 候选入口

候选实现 38 个 Codex case，包括输入、工具、审批、配置、上下文注入、子会话、历史和
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

从临时工作目录验证内置的 012 延迟审批材料。Testify 从 `PATH` 选择 Rust 入口，
并保留实际二进制和 SDK 身份。

```sh
export PATH="/tmp/testify-rust-install/bin:$PATH"
agent-sdk-testify prepare --agent codex --case SDK-CASE-012 \
	--fixture-dir /tmp/testify-rust-012
agent-sdk-testify official verify --fixture-dir /tmp/testify-rust-012 --timeout 45s
```

严格回放使用与录制匹配的 client。内置 Codex 材料使用 Rust client。已有的 Python
来源归档使用原 Python SDK 复验。
