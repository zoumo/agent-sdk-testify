---
type: Design
title: agent-sdk-testify 工具设计
status: stable
---

# agent-sdk-testify 工具设计

`agent-sdk-testify` 是面向多语言 SDK 的独立测试工具，公开业务入口只有
`record`、`verify` 和 `mock`。工具维护 case、官方 driver、录制材料、固定索引与
内置快照；消费方的业务测试编排和 SDK 仿真归消费方自己维护。

[查看三命令架构图](assets/three-commands.html)。

## 1. 核心边界

| 命令 | 职责 | 执行方与对端 | 输出结论 |
| --- | --- | --- | --- |
| `record` | 按本仓库 case 录制真实 Agent 交互 | 官方 driver → 官方 SDK → 录制代理 → 真实 Agent | 业务执行、采集完整性、退出和清理结果 |
| `verify` | 用官方 SDK 验证已有录制 | 官方 driver → 官方 SDK → 严格回放引擎 | 官方业务断言、协议匹配、完整消费、EOF 和清理是否通过 |
| `mock` | 使用录制充当 Agent | 消费者自己的测试 → 被测 SDK → mock | 每个连接的协议匹配、完整消费、EOF、退出与清理结果 |

`simulate`、Go driver 和 SDK 覆盖门禁不属于本工具。SDK 或 MASS 通过进程接口消费
`mock`，工具不替消费者执行其业务断言。

## 2. 仓库归属

| 内容 | 归属 |
| --- | --- |
| case、provider binding、输入与预期 | `scenarios/` |
| 官方 driver、Python wheel、driver 测试 | `drivers/` |
| 审核采用材料、固定索引、来源与哈希 | `recordings/` |
| 内置材料快照 | `builtin/` |
| 协议、录制、严格回放和 mock | `protocol/` 与根目录 Go 实现 |
| SDK 业务测试、仿真和覆盖汇总 | 各 SDK 消费者 |

```text
agent-sdk-testify/
  go.mod / go.sum / mise.toml
  main.go / record.go / verify.go / mock.go
  protocol/                 # 录制、严格回放、传输与命令接口
  scenarios/                # 权威 case、binding、输入与预期
  drivers/                  # 官方 driver 与 Python wheel
  recordings/               # 审核采用材料、固定索引及来源
  builtin/                  # 生成的发布快照
  docs/ / harness/ / scripts/ / README.md / LICENSE
```

生产代码和默认测试不导入任何 SDK 实现。官方 SDK 只是 `record`、`verify` 的显式
环境依赖，不在运行时自动安装。

## 3. 命令合同

### record

```sh
agent-sdk-testify record --provider codex --case SDK-CASE-008 \
  --cli /absolute/codex --config /absolute/selected-config \
  --output /private/new-capture
```

工具选择 case 和 provider binding，准备隔离环境，调用官方 SDK 对接真实 Agent，
保存原始双向帧、stderr、plan、来源 manifest、业务报告、采集和清理结果。

只有 `record` 可以连接真实 Agent，且必须由操作者显式执行。失败、未触发和不支持
的尝试同样保留原始证据。录制成功只代表本次采集达到其业务、采集与清理要求，仍需
`verify`。

### verify

```sh
agent-sdk-testify verify --recording /private/new-capture \
  --output /private/new-verification --timeout 2m
```

provider/case 从录制身份确定，工具只选择对应的官方 driver。对端使用严格回放引擎，
不连接真实 Agent。通过须同时满足来源和文件哈希有效、官方业务断言通过、所有连接
严格匹配并完整消费、EOF 正常、退出正常且自有进程清理成功。

验证不改写原始录制。验证证据与原件分开保存，并绑定录制、合同、工具和 driver 哈希，
同时记录实际安装的官方 SDK 包及版本。

### mock

普通场景可直接替换 Agent executable：

```sh
agent-sdk-testify mock --provider codex --case SDK-CASE-008 \
  --report /reports/mock-001.json
```

需要父进程环境、初始文件或多连接时，使用同一 `mock` 命令的配套模式：

```sh
agent-sdk-testify mock --list --provider codex --json
agent-sdk-testify mock --describe --provider codex --case SDK-CASE-008 --json
agent-sdk-testify mock --prepare --provider codex --case SDK-CASE-008 --output /reports/run-001
agent-sdk-testify mock --run /reports/run-001/run.json
agent-sdk-testify mock --finish --run /reports/run-001/run.json
```

这些模式互斥。默认启动模式的 stdout 只承载原生协议；查询、prepare 和 finish 的
结构化结果与协议进程分离，诊断写入 stderr 或报告文件。

- `list`：列出已发布场景和完整覆盖要求。
- `describe`：返回输入顺序、材料身份、断言输入和兼容性信息。
- `prepare`：创建自有隔离资源并生成 `run.json`。
- `run`：绑定已有 run，启动对应协议连接。
- `finish`：核验所有选中连接并清理 run 自有资源。

详细字段和错误语义见 [mock-run.md](mock-run.md) 与
[mock-catalog.schema.json](mock-catalog.schema.json)。

## 4. 材料与发布

材料链路：

```text
case → record → verify → 资格审核与采用 → 固定索引 → 内置快照 → mock
```

| 材料 | 保存规则 |
| --- | --- |
| 完整原始采集包 | Git 外私有归档，保留成功、失败、原件身份和哈希 |
| 审核采用材料 | `recordings/<provider>/<story>/<case>/<attempt>/` |
| 固定索引 | `recordings/index.json`，保留失败和缺口 |
| 内置快照 | 仅 `adoption=admitted` 且 `verification=passed` 的材料 |
| 发布清单 | 固定 tool version、来源、合同和逐文件哈希 |

采用成功不单独证明材料可以公开发布。内置集合不隐藏索引中的失败、未验证和不可用项。
构建拒绝材料缺失、哈希漂移或验证未通过，也不会为了形成成功集合而改写录制。

## 5. 环境与证据

工具准备 workspace、HOME、tmp 和 provider 配置目录。Codex 使用 `CODEX_HOME`，
Claude 使用 `CLAUDE_CONFIG_DIR` 并派生历史副本。消费者必须在启动 SDK 宿主前应用
`mock --prepare` 返回的环境和 workspace；仅设置 mock 子进程环境不足以配置父进程。

不复制完整用户环境、个人 skills、插件、记忆或认证。原始 raw/plan 保持不变；需要
改路径的历史只在派生副本中替换顶层身份，并保留来源与派生的双向哈希。

mock 不执行录制中的 shell、文件编辑或模型调用。消费者关闭自己的 Session/Runtime
后必须执行 `finish`；业务成功和协议成功分别判断，任何一方都不能替另一方背书。

## 6. 验证边界

默认验证全程离线，不启动真实 Agent，不新增录制。真实录制、官方 SDK 验证、SDK
仿真和产品 E2E 是不同证据层，分别报告。合成测试或局部通过不能升级为完整覆盖。

当前工具状态、材料缺口和已知官方环境限制分别由
[builtin/manifest.json](../builtin/manifest.json)、
[recordings/index.json](../recordings/index.json) 和
[official-environment.md](official-environment.md) 提供。
