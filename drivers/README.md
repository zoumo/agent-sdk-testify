# Agent SDK drivers

`drivers/` 包含 `official record`、`official verify` 使用的官方 SDK driver 及其 Python wheel。
driver 通过官方 SDK 执行一个 case 并给出业务断言；Go 工具负责 case 选择、
隔离环境、协议对端、进程管理和回执。

`agent mock` 消费者只使用 `agent-sdk-testify` 二进制，不需要安装本目录的 driver、
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
make bundle
bash scripts/with-agent-sdk-testify.sh env \
  PYTHONPATH=drivers/common:drivers/codex/official:drivers/claude/official \
  python3 -m unittest discover -s drivers/tests -v
bash scripts/with-agent-sdk-testify.sh \
  python3 -m unittest discover -s drivers/tests/scenarios -v
```

构建输出默认位于 `.artifacts/build/`：

- `agent-sdk-testify`
- `agent_sdk_testify_driver-*.whl`

`AGENT_SDK_TESTIFY_BUILD_DIR` 可指定另一个绝对目录。Python 3.11+ 需要
`setuptools>=68`；构建和离线测试不启动 SDK、真实 Agent 或模型。

## 官方 SDK 环境

只有 official record/verify 需要 Python 官方 SDK；agent record/mock 只使用工具二进制。使用
Python 3.11+、pip 和 `setuptools>=68` 构建本目录，再把 wheel 以 `--no-deps`
安装到显式环境，并将该环境的 `bin` 放入 `PATH`。

`requirements.lock` 和可选 extras 只是原始安装来源快照，不是当前可用性承诺。
`openai-codex==0.161.0` 和 `openai-codex-cli-bin` 当前不可从 PyPI 安装；按源码
VCS 声明解析依赖也会因 CLI-bin 版本不可用而失败。

已验证的材料环境使用 Claude `0.2.174` 和 Codex `0.0.0.dev0`。后者来自官方
源码 `rust-v0.161.0`、revision
`979011409de0a60b52f179721948e65531d26144`，其 pyproject 声明
`0.0.0-dev`；不要把它重新标成 `0.161.0`，也不要修改录制去匹配包名。

在显式目录中复现（网络准备由操作者单独执行）：

```sh
python3 -m venv /absolute/official-env
/absolute/official-env/bin/python -m pip install \
  'setuptools>=68' wheel 'uv_build>=0.11.19,<0.12' \
  'pydantic>=2.12' 'packaging>=26.2' 'claude-agent-sdk==0.2.174'
git clone --branch rust-v0.161.0 \
  https://github.com/openai/codex.git /absolute/codex-source
git -C /absolute/codex-source rev-parse HEAD
# 必须输出 979011409de0a60b52f179721948e65531d26144 后再安装。
/absolute/official-env/bin/python -m pip install \
  --no-deps --no-build-isolation /absolute/codex-source/sdk/python
/absolute/official-env/bin/python -m pip install \
  --no-deps /absolute/agent_sdk_testify_driver-*.whl
```

`--no-deps` 有意避免安装源码包的独立原生 CLI 依赖。agent record、official record 通过 `--cmd ABS` 和
`--` 后的原生 argv 显式指定 executable 及其参数；`official verify` 向 driver 提供公开
`agent mock --fixture-dir ...` 进程入口，由 driver 使用官方 SDK 启动，
不得回落到已安装的真实 Codex 或 Claude。源码 SDK、录制中的原生 CLI 和工具自身
是三个独立版本身份。

## 单请求协议

工具向 driver 提供一个请求文件，driver 完成后在输出目录写入 `result.json`。

| 请求字段 | 含义 |
| --- | --- |
| `case` | 本次执行的 case 身份 |
| `materials` | 已释放的 case 材料和合同 |
| `workspace` | 本次运行的可写工作区 |
| `peer` | 公开 agent record 或 agent mock 进程入口；远端 case 可带 `probe` |
| `output` | driver 结果、诊断和业务证据目录 |

`result.json` 至少包含 `status` 和可选 `message`。状态为 `passed`、`failed`、
`untriggered` 或 `unsupported`。driver 自己保存详细业务回执；stdout 不承载
业务协议，原生 SDK 进程使用独立管道。

官方录制和验证的 driver 都须报告实际启动的 agent execution/连接身份，以及 SDK
关闭和 agent 进程退出事实。official 命令据此核验结果；协议回执由 agent 命令自行
保存，不由 driver 或 official 命令代写。录制业务报告在采集结束后单独保存并关联
material，不回写不可变录制。职责和证据归属见[工具设计](../docs/design.md#official-verify)。

`official record` 提供的 `peer` 是现有进程监督器生成的 launcher。监督器为公开
`agent record` 接出普通 `--result-fd`，保存开始与最终通知；SDK 不需要继承该 FD。
运行 plan 的 `_agentResults` 按连接给出这些通知文件，driver 读取本次实际 execution
的采集证据，不查找最新目录。`main`、`probe` 共享一次 record execution，全部连接
完成后才发布材料。

官方采集材料保存 `source-plan.json` 原字节、派生 `plan.json` 的来源哈希、
`official-origin.json` 及逐连接启动证明；派生 plan 保留 SDK 实际发送的协议 cwd。
这些文件证明采集来源，随 export 携带，不证明父 driver 的业务通过。最终业务报告、
SDK 身份和进程结果保存在独立的 `official-records/<runId>/`，关联精确材料及哈希。
后续官方验证仍须产生自己的业务与协议证据，不能把采集来源当成业务成功。

driver 只执行一个请求，不实现 registry、双向命令、步骤 DSL 或 SDK 覆盖门禁。
case 输入、agent 参数和断言边界来自 [场景目录](../data/scenarios) 与
[case 规范](../docs/cases.md)。

<a id="fixtures-仿真录制与测试输入"></a>

## 录制与测试输入

`agent record` 的原始尝试保存在私有输出目录；经 `official verify`、资格审核和采用后，材料进入：

```text
data/<agentType>/<story>/<case>/<attempt>/
```

每个采用目录保留原始 `plan.json`、`manifest.json`、`raw/raw.jsonl`、业务报告、
官方验证证据、`review.json`、`adoption.json` 和必要的历史派生记录。原始字节和
attempt 身份不可改写；失败与未触发结果不伪装成通过。

公开 `agent record` 的材料报告保持 `captured`，最终官方业务报告保存在
`official-records`。显式采用时，`official verify` 将已核验的原编排、正常 record
execution、原生隔离准备回执及业务报告携带到验证证据和归档中；资格检查读取这些
实际事实，不补写材料中的旧 `summary.json` 或业务通过状态。采用前提见
[信任与入仓](../docs/design.md#信任与入仓)。归档保留隔离准备和最终业务事实的
实际文件与哈希，导出包仍只包含封闭材料，不携带原 fixture 的编排事实。
旧材料继续读取原有来源和业务证据布局。

以下输入不得混用：

- 真实录制：实际 Agent 交互，可进入验证和采用流程。
- 官方验证证据：官方 SDK 对已有录制的离线执行结果。
- 合成材料：人工构造的协议或错误路径输入，只用于代码测试。

当前材料状态、缺口和采用身份以
[固定索引](../data/index.json)和
[内置清单](../data/builtin/manifest.json)为准。文档中的 case 列表描述目标流程，
不代表每个 agent type 已有通过材料。
