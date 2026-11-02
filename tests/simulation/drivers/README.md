# Agent SDK drivers

<a id="repository-recording"></a>

`codex/official/` and `claude/official/` contain the native official-SDK cases;
Go SDK simulation flows and their Go entry remain owned by agent-sdk-go.
The official drivers dispatch single requests supplied by the
[agent-sdk-testify runner](../../../docs/legacy-cli.md). Each driver
owns its SDK operations and business assertions; the generic runner owns
environments, peer processes and result collection.

Build from the repository root with `bash scripts/simulation.sh build`. This builds
`.artifacts/simulation/{agent-sdk-testify,agent_sdk_testify_driver-*.whl}`;
`AGENT_SDK_TESTIFY_BUILD_DIR` selects another absolute output directory. No SDK or
Agent runs during build. The Python interpreter (`PYTHON`, default python3.11)
needs Python 3.11+ and setuptools>=68; dependency installation is explicit.

Install into a private Python 3.11+ environment:

```sh
python3.11 -m pip install '.artifacts/simulation/agent_sdk_testify_driver-0.1.0-py3-none-any.whl[recording]'
agent-sdk-testify-codex --request /absolute/request.json
```

Installed commands are `agent-sdk-testify-codex` and `agent-sdk-testify-claude`.
They execute one supplied request; Go prepares the environment and recording or
mock peer. Shared material helpers live under `common/`. The Python import remains
`record_user_story` for the existing recording-source readers. Older
manifest and schema readers retain existing dataset provenance. The catalog CLI
only reads material definitions; preparation, recording, replay, fixture copies
and native process cleanup belong to the Go runner. Saved Python driver snapshots
are evidence, not executable orchestration entry points.

SDK extras select dependencies without exact runtime-version gates.
`requirements.lock` is a known installation snapshot, not a runtime allowlist.
The driver writes `result.json` with `status` and `message`; detailed native
receipts and diagnostics remain driver-owned files in the same output directory.

For offline package and driver checks:

```sh
mise exec -- bash scripts/with-agent-sdk-testify.sh env PYTHONPATH=tests/simulation/drivers/common:tests/simulation/drivers/codex/official:tests/simulation/drivers/claude/official python3.11 -m unittest discover -s tests/simulation/drivers/tests -v
mise exec -- bash scripts/with-agent-sdk-testify.sh python3.11 -m unittest discover -s tests/simulation/drivers/tests/scenarios -v
```

The story/case table below describes available native flows, not successful
recordings or current Go SDK simulation coverage. Actual results belong to each
private attempt. See the [design](../../../harness/modules/agent-sdk-testify/design.md).

<a id="fixtures-仿真录制与测试输入"></a>

## 仿真录制与测试输入

<a id="stage-handoff"></a>

### 构建、执行与阶段交付

```sh
bash scripts/simulation.sh build
# Activate the venv containing the wheel and chosen SDK extra first.
bash scripts/simulation.sh record codex --case SDK-CASE-001 --materials "$PWD/tests/simulation/scenarios" --cli /absolute/codex --output /private/new-capture
bash scripts/simulation.sh replay codex --materials /private/new-capture --output /private/new-official-replay
# Run independently on already reviewed/admitted materials:
bash scripts/simulation.sh simulate --materials /private/admitted-capture --output /private/new-go-simulation
```

Only explicit record launches the selected native CLI. All runner flags, including
batch selection, pass through; heterogeneous batches can use the binary directly
with per-case drivers. The installed console command identities remain unchanged.

新样本顺序固定为：构建 → 官方 SDK 真实录制 → 官方 SDK 回放 → 资格校验及本地替换 → 当前 Go SDK 仿真。
录制、官方回放、Go 仿真分别生成 summary.json；result.json 是 driver 的业务结果，
summary 的 error 与每个协议回执共同决定运行是否有效，业务 passed 不能抵消传输失败。

交给后续审核／入库阶段的材料是原始 capture 目录（plan.json、request.json、
manifest.json、原始 raw、原生历史、driver 诊断与结果），以及独立官方 replay
目录及其 summary/result 和每个选中连接的完整消费回执。调用方记录这些目录、源码
revision 和使用的构建产物；source manifest 中的实际文件清单和哈希是来源依据。
保存失败目录及 native ID/cwd，不修改原始帧，不把旧 driver 快照当执行入口。
数据索引、可迁移性与覆盖门禁由下面的 corpus 入口负责。
`bash scripts/simulation.sh record` 自动执行官方回放、资格校验与本地替换；
Go 二进制的 `record --index` 执行同一自动流程；不带 `--index` 时只采集私有材料。
独立 `replay` 不更新索引。`corpus review|qualify|adopt|gate` 均由 Go 二进制实现。
Go SDK 通过不是有效样本入库条件；入库后的仿真失败保留有效样本并阻断 SDK 门禁。

Codex 008/009 的新 binding 显式提供当前模型的 `apply_patch` catalog，并关闭 shell；
不替换模型身份，也不把 command approval 当作 file approval。Codex 016/019 使用
V2 和 `multi_agent_v2_dynamic_tools`：0.163.0 的 V1 新子线程不继承 root dynamic tools，
见 [原生继承条件](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/core/src/agent/control/spawn.rs#L819-L829)。
019 的 Codex 子任务先调用本地 MCP completion barrier，再读取 fixture；等待不占用
Python SDK 的同步读循环。driver 在回答读取前订阅该子 turn，防止快完成事件丢失。
只有受控本地 `child_gate.wait_for_root` 配置为预批准，不修改其他 MCP 工具的审批策略。
Claude 019 在异步 Read 权限回调中等待对应 root result。两者均不靠固定延时制造顺序，
也不在提示词中提供待读取 token。离线测试只验证驱动、协议和时序，不代表新录制通过；
原有失败材料和必需覆盖门禁保持不变。

### 审核采用和日常门禁

```sh
# Normal repository recording: capture, official replay, qualification, replacement.
bash scripts/simulation.sh record codex --materials tests/simulation/scenarios --case SDK-CASE-001 --cli /path/to/codex --config /private/config --model provider/model
# Reuse a completed capture and official replay without new model calls:
bash scripts/simulation.sh qualify --source /private/capture --replay /private/official-replay --review /private/qualification.json
bash scripts/simulation.sh adopt --source /private/capture --replay /private/official-replay --review /private/qualification.json
# Every code change: rebuild current Go binaries and simulate the fixed corpus.
bash scripts/simulation.sh gate
mise exec -- bash scripts/check-offline.sh
```

`qualify` 要求带录制时隔离标记的新采集，校验官方 SDK 录制和回放，生成机器资格记录，
不填写人工审核标记，不执行内容扫描。旧版未隔离采集不能走自动采用路径。
只选择固定允许范围内的必需回归材料；资格有效不等于内容已脱敏或可任意公开。
`record` 为每个通过的 case 执行这条路径，替换现有本地回归数据并保存 admission-summary.json。
批次部分失败时仍采用通过的 case，失败 case 保留旧版本，整批返回非零。不自动 commit/push。
`review` 仍可生成未批准的人工审核清单，不自动确认来源或发布权限。审核者核对完整来源、原生执行身份、
官方 SDK 身份、业务结果、敏感内容和每个 `publishFiles`；三个审核标记须明确为 true。
`captureFiles` 冻结完整原件，`replayFiles` 冻结官方业务报告、summary/result 和所有连接回执。
官方 replay 的 summary 必须绑定原始 plan/manifest/report/result/summary；旧回执缺少绑定时，
使用当前官方 driver 重新离线回放，不能补写成功回执。
机器规则不能识别蓄意伪造的整套来源声明；真实性依赖具名的来源审核，不能用合成测试替代。

`adopt` 先将完整原件保存到持久数据根的 `archives/<attempt>/capture/`，拒绝的尝试也留存。
数据根与录制入口一致：AGENT_SDK_TESTIFY_DATA_DIR，或 XDG_DATA_HOME/agent-sdk-testify，
默认 ~/.local/share/agent-sdk-testify；原件归档不依赖当前 worktree 的存续。
可用 `--archive` 选择另一个持久私有目录。在 checkout 内只允许被 Git 忽略的 `.artifacts`。
原始采集包不会整体发布，只有审核选中的必需材料进入
`tests/simulation/recordings/<provider>/<story>/<case>/<attempt>/`，并更新固定 index。
`adoption.json` 关联原件哈希、归档 ID 和公开文件清单；完整 review 同时保存。
入库原件不做字节转换，初始文件来自 plan，历史来自经过校验的 config/history-seed 副本。
两类官方驱动均在新 workspace 启动 SDK 子进程，Codex 原生请求的 cwd 参数仍保留原值。
Claude 运行时也使用新 workspace，并按新目录键派生本地历史副本，仅替换顶层 cwd。
原始 cwd/ID、raw、plan 和历史保持原样；materialization.json 保存来源／派生路径及哈希，
官方回放审核和 Go 门禁都校验该记录及派生文件，支持旧 cwd 已不存在的环境。
已有入库样本会被通过验证的新样本替换。旧样本及旧索引项先存入私有归档的
previous-regression/ 和 previous-entry.json；新文件写入完成后原子切换索引，再移除旧回归目录。
新的失败尝试或索引切换失败不会删除旧样本。Go 仿真不参与采用资格判定。
`--index` 支持在独立目录验证完整固定索引，不支持缩小必需集合。

仓库 shell 的 `gate` 总是构建当前 Go 工具和 SDK driver，校验固定索引与采用哈希，
再用统一运行器批量 simulate。Go 二进制的 `corpus gate --sdk-driver` 使用显式指定的构建产物，
不依赖 checkout 或编译器；`--source-root` 可选，仓库 shell 用它绑定源码文件哈希和 revision。
它不要求安装官方 SDK、认证或真实 CLI。覆盖报告独立保存在
`.artifacts/simulation/gates/<attempt>/coverage.json`，记录当前源码及二进制哈希。
能力要求 requirement 只有 required/optional，由共享 case 的 required 字段及 provider binding 的显式 required 覆盖决定；
provider 路径 planned/unsupported 不改变要求。adoption 是采用状态（admitted/missing/untriggered），
verification 是业务验证（passed/failed/unverified/blocked/unsupported）；非 passed 必须有
verificationReason，passed 必须有已采用材料。coverage 另记本轮 simulationStatus，最终 status
不能把已知失败或未知业务证据因回放成功提升为 passed。
必需项须同时满足已采用、业务验证 passed、本轮 Go 仿真 passed；缺任一条件返回非零。
可选项缺失、业务失败或仿真失败仅报告，不阻断必需验收；任何已采用材料的来源／哈希损坏仍失败。
采用后 Go 失败仍保留样本；应检查 SDK/driver，不能改帧、放宽协议匹配或弃样。
`check-offline.sh` 包含该门禁，CI 即使缺样本仍继续打包、race 和 vet，并保存覆盖与诊断。
固定覆盖为 64 required、8 optional（36 case × 两个 provider）；真实样本及采用哈希见
[固定索引](../recordings/index.json)。003、025、026 可选；027 思考强度仍为必需。
Claude 017、032 为可选，保留 unsupported／blocked 状态；Codex 对应 case 仍必需。
目前只有 Codex 012 的必需验证未满足：受官方 Python SDK 审批分发限制，保持 blocked。
Claude 003 文档输入的旧结果称未收到内容，使用 DeepSeek 补录仍未识别文档，保留旧样本并标记 failed；Claude 026 只确认设置回执，
实际预算生效范围未知，标记 unverified。原始报告和录制不改写；复现它们不等于业务目标通过。
003 的 DeepSeek 尝试在原生进程边界包含完整 base64 PDF，解码后与生成输入一致，
独立 PDF 解析可读出预期文字；这些证据仅排除了 driver 未发送文档或 PDF 无法解析，尚未定位原生处理或上游模型的失败原因。
不能以本地提取文字代替原生文档输入，或把后续普通终态当作文档读取成功。
Codex 025 的旧样本配置的是 effort，不证明独立思考模式；该模式路径与业务验证均标记
unsupported，保留原始材料。027 的必需要求不受影响。
两端 002、004 的旧结果未识别图片，已以 `theta/DeepSeek-V4.1-Flash-1017` 补录替换。
媒体驱动要求回答附件本身的信息，不能以提交内容块和普通终态作为读取证明。
两端 022～024 已补录从 `theta/glm-5.3` 到 `theta/DeepSeek-V4.1-Flash-1017` 的不同模型请求。
022、023 分开保留回执与后续原生模型观察；Codex 仅证明 thread settings，
Claude 保留带 theta 前缀的请求及不带前缀的原生 assistant model，不伪装成同一个字段。
024 仅证明执行期间请求、回执和当前终态，不承诺当前执行立即切换模型。
两端 028 验证配置原生透传、关联成功响应处理和后续执行完成，现有材料满足该范围，标记 passed；
实际展示效果不作为门禁条件，不补录、不改写历史报告。原生错误不得当作成功回执。
Codex 012 仍阻断必需门禁；实时结论以固定索引及本轮覆盖报告为准。
语义、要求和业务状态同步到场景与索引；历史 plan、manifest、原生版本、模型及来源哈希不追改。
corpus 可同时保留不同模型的录制，不因本轮补录模型变化而重录所有既有合格样本。
各 case 的通过证据、配置生效范围与不包含的承诺以[流程语义](../../../harness/sdk/validation-cases.md#流程语义)
为准；共享 expect.scope 仅概述范围，不代替驱动中的实际断言。
工具的合成测试通过不代表真实 corpus 通过。

未指定 --output 时，输出保存在 AGENT_SDK_TESTIFY_DATA_DIR/attempts/<mode>/attempt-*；
数据根默认 XDG_DATA_HOME/agent-sdk-testify，或 ~/.local/share/agent-sdk-testify。
新输出目录不会覆盖既有目录；临时测试资源由测试显式选择。


仿真测试的来源是实际 Agent 交互。已录制样本的来源和审核记录随样本保存，
本次验证结果以 gate 的覆盖报告为准。[操作说明](#repository-recording)描述新录制与回放。

<a id="fixtures-私有仿真数据"></a>

### 私有仿真数据

私有数据按 `<story-id>/<case-id>/` 保存；文件清单见[数据合同](../../../harness/modules/agent-sdk-testify/design.md#story-数据格式与存储)。

配置和认证副本按录制流程清理。私有诊断不随官方验证通过而进入回归区；不得直接发布完整采集包。
数据来源、哈希与遗漏以该次 manifest 为准；本地替换不自动触发远程发布。
规范见[仿真工具](../../../harness/modules/agent-sdk-testify/design.md#story-官方-sdk-仿真测试)和[布局](../../../harness/architecture.md#layout-code-layout-standards)。

<a id="fixtures-代码集成测试输入"></a>

### 代码集成测试输入

这些材料均为人工构造的协议，不是仿真录制，也不计入故事验收。

| 输入 | 代码归属与来源 |
|---|---|
| Codex 生命周期与故障、原生问题 | [生命周期](https://github.com/zoumo/agent-sdk-go/blob/428263bd594bcd2586049ba38d01c5573284e0ca/codex/tests/testdata/integration/lifecycle.jsonl)、[问题](https://github.com/zoumo/agent-sdk-go/blob/428263bd594bcd2586049ba38d01c5573284e0ca/codex/tests/testdata/integration/question.jsonl) |
| Claude 任务失败 | [任务失败](https://github.com/zoumo/agent-sdk-go/blob/428263bd594bcd2586049ba38d01c5573284e0ca/claude/tests/testdata/integration/task-failure.jsonl) |
| 共享原生对端 | [tests/integration](https://github.com/zoumo/agent-sdk-go/blob/428263bd594bcd2586049ba38d01c5573284e0ca/tests/integration/peer_test.go)、[会话对端](https://github.com/zoumo/agent-sdk-go/blob/428263bd594bcd2586049ba38d01c5573284e0ca/tests/integration/session_peer_test.go) |
| Claude 子任务示例 | [合成事件](https://github.com/zoumo/agent-sdk-go/blob/428263bd594bcd2586049ba38d01c5573284e0ca/claude/examples/subagents-offline/testdata/events.ndjson) |

Go fixture 适配器读取同一份嵌入数据，协议匹配由 `protocol/` 实现。
专项测试直接验证 SDK 公开接口；不生成按 story/case 编号组织的合成回执。

<a id="catalog-仿真测试流程目录"></a>

## 仿真测试流程目录

[仓库场景目录](../scenarios)包含 8 个真实用户故事、36 条完整流程。主规范见[用户故事](../../../harness/sdk/validation-cases.md#stories-sdk-用户故事)及[case 语义](../../../harness/sdk/validation-cases.md)。

`live-selected-path` 表示驱动提供该真实流程；`planned` 表示尚未实现；`unsupported` 表示已知原生能力不支持。
这些状态不代表录制或回放已通过。实际结果由每次私有数据集中的来源、报告与回放回执证明。
待实现和不支持的理由保存在 provider 绑定及 catalog 输出中；它们不执行替代流程。

| Case ID | Story ID | 能力要求 | 完整流程 | Codex 路径 | Claude 路径 | 来源 |
|---|---|---|---|---|---|---|
| SDK-CASE-001 | SDK-US-001 | required | 连续提交文本输入并在结果后继续观察 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-001.json) |
| SDK-CASE-002 | SDK-US-001 | required | 提交图片并读取结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-001.json) |
| SDK-CASE-003 | SDK-US-001 | optional | 提交文档并读取结果 | unsupported | live-selected-path | [配置](../scenarios/SDK-US-001.json) |
| SDK-CASE-004 | SDK-US-001 | required | 提交混合结构化内容并读取结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-001.json) |
| SDK-CASE-005 | SDK-US-001 | required | 执行中追加输入并保留原生调度结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-001.json) |
| SDK-CASE-006 | SDK-US-002 | required | 观察文件工具的执行与任务完成 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-002.json) |
| SDK-CASE-007 | SDK-US-002 | required | 处理应用工具请求并等待任务完成 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-002.json) |
| SDK-CASE-008 | SDK-US-003 | required | 拒绝文件写入审批并观察后续结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-003.json) |
| SDK-CASE-009 | SDK-US-003 | required | 批准文件写入审批并观察后续结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-003.json) |
| SDK-CASE-010 | SDK-US-003 | required | 批准命令审批并观察后续结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-003.json) |
| SDK-CASE-011 | SDK-US-003 | required | 拒绝命令审批并观察后续结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-003.json) |
| SDK-CASE-012 | SDK-US-003 | required | 持有审批期间观察事件与控制回执 | planned | live-selected-path | [配置](../scenarios/SDK-US-003.json) |
| SDK-CASE-013 | SDK-US-004 | required | 回答选择题并继续任务 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-004.json) |
| SDK-CASE-014 | SDK-US-004 | required | 提供自由文本答案并继续任务 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-004.json) |
| SDK-CASE-015 | SDK-US-004 | required | 回答一个请求中的多道问题 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-004.json) |
| SDK-CASE-016 | SDK-US-005 | required | 观察直接子 Agent 的身份、输出与结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-005.json) |
| SDK-CASE-017 | SDK-US-005 | required (claude optional) | 观察嵌套子 Agent 的实际父子关系 | live-selected-path | unsupported | [配置](../scenarios/SDK-US-005.json) |
| SDK-CASE-018 | SDK-US-005 | required | 观察并行子 Agent 的独立输出 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-005.json) |
| SDK-CASE-019 | SDK-US-005 | required | 根任务结束后继续观察后台子 Agent | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-005.json) |
| SDK-CASE-020 | SDK-US-005 | required | 选择子详细交付策略并观察实际输出 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-005.json) |
| SDK-CASE-021 | SDK-US-006 | required | 创建会话时选择模型并执行任务 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-022 | SDK-US-006 | required | 首次输入前修改模型并核对确认、生效顺序与可观察配置 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-023 | SDK-US-006 | required | 两次执行之间修改模型并继续输入 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-024 | SDK-US-006 | required | 执行期间请求修改模型并观察结果 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-025 | SDK-US-006 | optional | 创建会话时选择思考模式 | unsupported | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-026 | SDK-US-006 | optional | 修改原生思考预算并执行任务 | unsupported | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-027 | SDK-US-006 | required | 选择原生 effort 并执行任务 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-028 | SDK-US-006 | required | 验证思考展示配置透传和原生响应处理 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-029 | SDK-US-006 | required | 配置根会话并观察子 Agent 的实际继承情况 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-006.json) |
| SDK-CASE-030 | SDK-US-007 | required | 在执行进度后中断并读取确认及终态 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-007.json) |
| SDK-CASE-031 | SDK-US-007 | required | 关闭拥有的会话并确认交互与进程结束 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-007.json) |
| SDK-CASE-032 | SDK-US-007 | required (claude optional) | 关闭远端连接并保持远端服务可用 | live-selected-path | planned | [配置](../scenarios/SDK-US-007.json) |
| SDK-CASE-033 | SDK-US-008 | required | 独立发现会话、读取元数据和主会话内容 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-008.json) |
| SDK-CASE-034 | SDK-US-008 | required | 独立发现并读取保存的子 Agent 内容 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-008.json) |
| SDK-CASE-035 | SDK-US-008 | required | 发现当前 Session 观察前已有的子 Agent | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-008.json) |
| SDK-CASE-036 | SDK-US-008 | required | 按原生分页或切片浏览保存的记录与内容 | live-selected-path | live-selected-path | [配置](../scenarios/SDK-US-008.json) |

专项测试直接归属代码集成测试，不出现在本表。
