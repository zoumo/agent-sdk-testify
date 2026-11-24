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
| `claude/typescript/` | Claude TypeScript SDK driver，支持 case 042 |
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

Codex 审批 case 039～041 使用 `codex-cli 0.166.0` 及官方 SDK 源码
`rust-v0.166.0`（`6ff1e1ca3b30fc8b914720681fcf5fe62e95f856`），其安装版本同为
`openai-codex 0.0.0.dev0`。复录和验证这些 case 时使用该源码版本。039 和 040 的
录制需要 `python3` 启动隔离的合成 stdio MCP 工具。

Claude case 042 使用 `Claude Code 2.1.293` 和 `@anthropic-ai/claude-agent-sdk@0.3.293` 的
`onElicitation`。在显式 Python 环境的根目录安装 npm 包，并将 Node.js 放入 PATH。
安装无需 SDK 自带的原生 CLI，录制使用 `--cmd` 指定的独立 CLI，验证使用公开 mock。

```sh
npm install --prefix /absolute/official-env --ignore-scripts --omit=optional @anthropic-ai/claude-agent-sdk@0.3.293
```

### 安装独立的 Claude TypeScript driver

使用 Node.js 22 或更新版本。在 `drivers/claude/typescript/` 中构建 npm tarball。
tarball 包含官方 SDK 和运行时依赖。公开 agent 命令提供原生进程入口。

```sh
npm ci --ignore-scripts --omit=optional
npm pack
npm install --offline --global --ignore-scripts --omit=optional \
  --prefix /absolute/claude-ts /absolute/agent-sdk-testify-claude-0.1.0.tgz
```

将 `/absolute/claude-ts/bin` 放在 wheel 所在路径前。使用工具的 `prepare` 创建 Claude
case 042 fixture，再运行 `official verify`。driver 从本次安装的 SDK 读取业务报告中的版本。

## 按 case 录制

前置条件是已安装工具、官方 driver 和所选官方 SDK。将官方环境的 `bin` 加入 `PATH`。
准备可运行的原生 CLI，以及父目录已存在的绝对 fixture 路径。`prepare` 创建 fixture。
[官方 SDK 环境](#官方-sdk-环境)

1. 选择 agent type 和 case，创建 fixture。`--model`、`--initial-model` 和 `--history-seed`
   属于准备步骤。Testify 从该 agent type 的原生配置位置发现允许的配置和认证。
   [场景目录](../data/scenarios)、[环境与隔离](../docs/design.md#环境与隔离)

	```sh
	agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
		--fixture-dir /private/fixture
	```

2. 指定原生 CLI 的绝对路径。在 `--` 后附上原生启动参数。若需设置 `--timeout`，
   将它放在 `--` 前。官方 driver 通过 SDK 启动录制代理。

	```sh
	agent-sdk-testify official record --fixture-dir /private/fixture \
		--cmd /absolute/codex -- app-server --listen stdio://
	```

3. 检查 driver 业务报告和录制回执。录制成功后，Testify 更新 current recording。
   官方录制结果引用实际的协议 execution。失败和未触发的尝试保留诊断。
   [官方录制合同](../docs/design.md#official-record)
4. 验证同一 fixture 中的 current recording。需要在另一个 fixture 使用时，先导出材料。
   [离线验证与采用](#官方离线验证与采用)、[导出与复用](../docs/using-mock.md#导出与复用)

上下文注入使用 `SDK-CASE-037` 检查空闲状态，使用 `SDK-CASE-038` 检查执行中状态。
Codex 和 Claude driver 都从原始录制核对注入与后续查询的边界。下面的命令录制 Claude 的空闲场景。
[上下文注入验收](../docs/cases.md#中断关闭与独立查询)

```sh
agent-sdk-testify prepare --agent claude --case SDK-CASE-037 \
	--fixture-dir /absolute/new-context-fixture
agent-sdk-testify official record --fixture-dir /absolute/new-context-fixture \
	--cmd /absolute/claude -- \
	--input-format stream-json --output-format stream-json --verbose \
	--include-partial-messages --permission-prompt-tool stdio
agent-sdk-testify official verify --fixture-dir /absolute/new-context-fixture
```

## 官方离线验证与采用

使用匹配材料身份的官方 driver 和 SDK。`official verify` 按材料选择 driver，
由 driver 启动 `agent mock`。验证过程使用录制回放。
[官方验证合同](../docs/design.md#official-verify)

1. 如果材料已在本次录制的 fixture 中，沿用该 fixture。如果材料来自另一目录，
   先导入到新的 fixture。

	```sh
	agent-sdk-testify prepare --recording /private/new-capture \
		--fixture-dir /private/imported-fixture
	```

2. 验证 current recording。示例使用导入的 fixture。

	```sh
	agent-sdk-testify official verify --fixture-dir /private/imported-fixture \
		--timeout 2m
	```

3. 读取 `packages/<materialId>/verify/<verificationId>/verification.json` 和业务报告。
   验证通过要求业务断言和所引用的全部 mock execution 均通过。查看连接回执，确认严格
   匹配、完整消费、EOF、退出和清理。

具备采用资格的材料可在验证时显式启用采用。`--index` 启用采用。省略 `--scenarios` 时，
工具在 index 目录旁的 `../scenarios` 查找场景。
[采用与发布合同](../docs/design.md#信任与入仓)

```sh
agent-sdk-testify official verify --fixture-dir /private/imported-fixture \
	--index /absolute/data/index.json \
	--scenarios /absolute/data/scenarios --archive /private/archives
```

读取 `admission-summary.json` 确认采用结果。采用失败时，工具保留验证证据和原索引。
发布材料仍需满足发布要求。

## 单请求协议

工具向 driver 提供一个请求文件，driver 完成后在输出目录写入 `result.json`。

| 请求字段 | 含义 |
| --- | --- |
| `case` | 本次执行的 case 身份 |
| `materials` | 本次 runtime 下的输入目录，driver 从其中 case.json 读取 canonical plan 的运行投影；不是 package/material 根 |
| `runtime` | 版本化运行配置的绝对路径，包含本次 workspace、HOME、config、raw、tmp、output、可选 seed root、peer/agentResults 和 replay 状态 |

`result.json` 至少包含 `status` 和可选 `message`。状态为 `passed`、`failed`、
`untriggered` 或 `unsupported`。driver 自己保存详细业务回执；stdout 不承载
业务协议，原生 SDK 进程使用独立管道。

material plan 只提供回放与资格所需的协议身份和历史绑定；`root/home/config/raw/tmp`、
driver workspace、seed root、结果位置和公开 peer 命令只由 runtime 配置提供。
driver 必须分别校验这两个输入。runtime 配置当前为 schema 1；未知、缺失或混合版本
在启动 SDK 前失败，不能从 material plan 猜测运行字段。request、runtime 和 case
派生的运行时输入默认只存在于 runtime，不进入持久 package。

官方 driver 与外层调用方的宿主串行责任遵循
[SDK 宿主的运行时入口](../docs/design.md#sdk-宿主的运行时入口)，不能把 agent claim
锁当作 driver 启动前的互斥。官方录制和验证的 driver 都须报告实际启动的 agent
execution/连接身份，以及 SDK 关闭和 agent 进程退出事实。official 命令据此核验结果；协议回执由 agent 命令自行
保存，不由 driver 或 official 命令代写。录制业务报告在采集结束后单独保存并关联
material，不回写不可变录制。职责和证据归属见[工具设计](../docs/design.md#official-verify)。

`official record` 提供的 `peer` 是现有进程监督器生成的 launcher。监督器为公开
`agent record` 接出普通 `--result-fd`，保存开始与最终通知；SDK 不需要继承该 FD。
`runtime.agentResults` 按连接给出通知文件；driver 的兼容运行视图映射为 `_agentResults`，仅存于内存，driver 读取本次实际 execution
的采集证据，不查找最新目录。schema 3 的通知引用以 package 为根；runtime 的
`fixtureDir` 显式提供当前 fixture，driver 核对 fixture/material/execution 身份、
execution 哈希及持久 result 后解析引用，不从通知路径推测 fixture。`main`、`probe` 共享一次 record execution，全部连接
完成后才发布材料。

agent record 领取 execution 时创建 `packages/<materialId>/`；成功的 material core
位于该 package 的 `material/`，并保存 `source-plan.json` 原字节、唯一派生 `plan.json`、
`official-origin.json` 及逐连接 `origin/<connectionId>.json` 启动证明。派生 plan
保留 SDK 实际发送的协议 cwd；这些文件证明采集来源，随 material export 携带，
不证明父 driver 的业务通过。

official record 通过普通 result fd 取得精确 material、execution 和 connection ID，
只把 runtime staging 中本次 `record/<runId>/` 结算到该 package。official verify
通过公开 mock 在同一 package 产生 execution，再新增独立
`verify/<verificationId>/`。两者都不扫描最新目录，不创建 official 专用协议执行，
也不在 material 或 record 域复制第二份 receipt。材料提交后父 driver 失败只使
record run 失败，不回写 material 或回滚 current。未领取 execution 的失败、输出结算和
崩溃恢复统一遵循[封存、发布与恢复](../docs/design.md#封存发布与恢复)。

driver 只执行一个请求，不实现 registry、双向命令、步骤 DSL 或 SDK 覆盖门禁。
case 输入、agent 参数和断言边界来自 [场景目录](../data/scenarios) 与
[共同验收](../docs/cases.md)。

<a id="fixtures-仿真录制与测试输入"></a>

## 录制与测试输入

`agent record` 的原始尝试保存在 package 的 execution 证据域；经 `official record`、
`official verify`、资格审核和显式采用后，case 进入：

```text
data/<agentType>/<story>/<case>/<attempt>/
  material.json
  manifest.json
  plan.json
  raw/...
  history/...
  ...
```

attempt 根就是 material 根，保留原始 plan/raw、来源和历史绑定字节。完整证据包在
采用前完成资格校验，所选 execution、record、verify 和 review/adoption 只留在
私有归档，不进入仓库或内置快照。固定索引保存材料哈希与资格摘要；消费者 mock 不
依赖外部归档。失败与未触发 attempt 留在私有诊断中，不作为成功材料发布。
完整证据闭包和采用规则见[信任与入仓](../docs/design.md#信任与入仓)。

公开 `agent record` 的材料报告保持 `captured`，最终官方业务报告保存在同一
package 的 `record/<runId>/`。显式采用时，工具选择已核验的原编排、
正常 record execution、原生隔离准备回执及业务报告所在的独立证据域；资格检查
读取这些实际事实，不补写 material 中的旧 `summary.json` 或业务通过状态。采用前提见
[信任与入仓](../docs/design.md#信任与入仓)。归档保留隔离准备和最终业务事实的
实际文件与哈希，export 仍只复制封闭 material，不携带 package 编排事实或原 fixture
路径。
旧材料继续读取原有来源和业务证据布局。
`--history-seed` 用于新的真实录制时仍须提供外部完整合格证据包，不能用精简后的
standalone core 代替原录制业务证明。

035 自行创建前置父子历史，不需要 `--history-seed`，也不读取 034 材料或资格归档。
prepare 保持离线；official record 内的 main 连接执行一次 setup 输入，确认真实
子完成和 native history 保存后关闭。probe 在同一 execution 的同一父身份上
建立新的正式观察入口，保持零输入。材料中保存 setup 的原始协议、历史、
身份和阶段哈希；official verify 离线重放 setup 与正式观察，并复核阶段顺序。

以下输入不得混用：

- 真实录制：实际 Agent 交互，可进入验证和采用流程。
- 官方验证证据：官方 SDK 对已有录制的离线执行结果。
- 合成材料：人工构造的协议或错误路径输入，只用于代码测试。

当前材料状态、缺口和采用身份以
[固定索引](../data/index.json)和
[内置清单](../data/builtin/manifest.json)为准。文档中的 case 列表描述目标流程，
不代表每个 agent type 已有通过材料。
