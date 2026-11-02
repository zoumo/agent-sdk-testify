# Agent SDK drivers

`drivers/` 包含 `record`、`verify` 使用的官方 SDK driver 及其 Python wheel。
driver 通过官方 SDK 执行一个 case 并给出业务断言；Go 工具负责 case 选择、
隔离环境、协议对端、进程管理和回执。

`mock` 消费者只使用 `agent-sdk-testify` 二进制，不需要安装本目录的 driver、
官方 SDK 或 Python。

## 布局

| 路径 | 内容 |
| --- | --- |
| `codex/official/` | Codex 官方 SDK driver |
| `claude/official/` | Claude 官方 SDK driver |
| `common/` | 场景、原生输入和共享材料读取 |
| `tests/` | driver 的离线单元和进程测试 |
| `pyproject.toml` | wheel 定义和两个命令入口 |

两个已安装命令是 `agent-sdk-testify-codex` 和 `agent-sdk-testify-claude`。

## 构建与测试

从仓库根目录执行：

```sh
bash scripts/simulation.sh build
mise exec -- bash scripts/with-agent-sdk-testify.sh env \
  PYTHONPATH=drivers/common:drivers/codex/official:drivers/claude/official \
  python3.11 -m unittest discover -s drivers/tests -v
mise exec -- bash scripts/with-agent-sdk-testify.sh \
  python3.11 -m unittest discover -s drivers/tests/scenarios -v
```

构建输出默认位于 `.artifacts/simulation/`：

- `agent-sdk-testify`
- `agent_sdk_testify_driver-*.whl`

`AGENT_SDK_TESTIFY_BUILD_DIR` 可指定另一个绝对目录。Python 3.11+ 需要
`setuptools>=68`；构建和离线测试不启动 SDK、真实 Agent 或模型。

## 安装

使用 `--no-deps` 把 wheel 安装到显式 Python 环境，并把该环境的 `bin` 放入
`PATH`。官方 SDK 和原生 CLI 需要单独准备；完整步骤见
[官方环境说明](../docs/official-environment.md)。

`requirements.lock` 和可选 extras 只是安装来源快照，不是当前可用性承诺。
wheel 安装成功不证明其外部 SDK 依赖、认证或原生 Agent 可用。

## 单请求协议

工具向 driver 提供一个请求文件，driver 完成后在输出目录写入 `result.json`。

| 请求字段 | 含义 |
| --- | --- |
| `case` | 本次执行的 case 身份 |
| `materials` | 已释放的 case 材料和合同 |
| `workspace` | 本次运行的可写工作区 |
| `peer` | 录制代理或 mock 的原生进程入口；远端 case 可带 `probe` |
| `output` | driver 结果、诊断和业务证据目录 |

`result.json` 至少包含 `status` 和可选 `message`。状态为 `passed`、`failed`、
`untriggered` 或 `unsupported`。driver 自己保存详细业务回执；stdout 不承载
业务协议，原生 SDK 进程使用独立管道。

driver 只执行一个请求，不实现 registry、双向命令、步骤 DSL 或 SDK 覆盖门禁。
case 输入、provider 参数和断言边界来自 [场景目录](../scenarios) 与
[case 规范](../harness/cases.md)。

<a id="fixtures-仿真录制与测试输入"></a>

## 录制与测试输入

`record` 的原始尝试保存在私有输出目录；经 `verify`、资格审核和采用后，材料进入：

```text
recordings/<provider>/<story>/<case>/<attempt>/
```

每个采用目录保留原始 `plan.json`、`manifest.json`、`raw/raw.jsonl`、业务报告、
官方验证证据、`review.json`、`adoption.json` 和必要的历史派生记录。原始字节和
attempt 身份不可改写；失败与未触发结果不伪装成通过。

以下输入不得混用：

- 真实录制：实际 Agent 交互，可进入验证和采用流程。
- 官方验证证据：官方 SDK 对已有录制的离线执行结果。
- 合成材料：人工构造的协议或错误路径输入，只用于代码测试。

当前材料状态、缺口和采用身份以
[固定索引](../recordings/index.json)和
[内置清单](../builtin/manifest.json)为准。文档中的 case 列表描述目标流程，
不代表每个 provider 已有通过材料。
