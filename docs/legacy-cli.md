# Copied CLI reference (migration baseline)

This reference describes the inherited CLI. The approved design governs the target
record/verify/mock interface; T1 changes module, layout and test ownership only.
SDK simulation examples require an explicitly supplied SDK-owned driver.

# agent-sdk-testify

Go prepares environments and runs one native SDK test driver per case. The driver
owns SDK operations and business assertions; Go reads its final result and checks
process/capture/mock completion. No command/event negotiation or driver catalog.

Build the tool, Go simulation driver and official-driver wheel together:

```sh
bash scripts/simulation.sh build
```

The default build directory is `.artifacts/simulation`; set
`AGENT_SDK_TESTIFY_BUILD_DIR` to use another absolute directory. `PYTHON` selects
Python 3.11+ with setuptools>=68 installed. Builds do not run an Agent or install
SDK dependencies. Both driver implementations live together under
`tests/simulation/drivers/<agent>/official` (Go SDK flows remain in agent-sdk-go). Shared case materials remain in
`tests/simulation/scenarios`; SDK targeted integration remains in `tests/integration`.

Use any installed driver executable. For Python, install the official SDK extras
from [Agent drivers](../tests/simulation/drivers/README.md) and select its interpreter/script
or the installed `agent-sdk-testify-codex` or `agent-sdk-testify-claude` command. TS/Rust drivers can implement
the same request/result convention without adding language logic to Go.

## Protocol operations

One binary provides case orchestration and independent protocol operations.
The runner launches its own executable by default; `--peer` optionally selects
another `agent-sdk-testify` build. SDK tests use `AGENT_SDK_TESTIFY_BIN`.
The [module design](../harness/modules/agent-sdk-testify/design.md) owns the contracts.

```sh
agent-sdk-testify protocol mock --provider codex --scenario /absolute/raw.jsonl
agent-sdk-testify protocol record --output /absolute/new-capture -- /absolute/native-cli
agent-sdk-testify protocol connect --help
```

Protocol operations need no driver or case materials. Protocol stdout contains
only native messages; diagnostics use stderr and separate reports. The persisted
`agent-mock-jsonl` v1 format and original recording bytes remain unchanged.

## Reuse an existing recording

```sh
agent-sdk-testify replay \
  --materials /absolute/private/SDK-US-001/SDK-CASE-001 \
  --driver /absolute/venv/bin/agent-sdk-testify-claude \
  --output /absolute/private/new-official-replay

agent-sdk-testify simulate \
  --materials /absolute/private/SDK-US-001/SDK-CASE-001 \
  --driver /absolute/bin/sdk-driver \
  --output /absolute/private/new-go-simulation
```

Both commands are offline. They use the current selected driver and mock, require
no native CLI/credentials, and do not require matching old tool/SDK versions.
Existing materials can be simulated independently. New samples follow build →
official record → official replay → source/content review → dataset admission →
current Go simulation. Go passing is not an admission condition; an admitted
sample is retained when Go simulation fails. Required failures block the SDK gate;
optional failures remain reported without blocking required acceptance. Source integrity
is mandatory for every admitted sample. Existing plan.json supplies
data and native protocol identities, not a frozen-driver execution requirement.
Existing source manifest hashes are checked for original frames, plan data and
saved history; this does not lock the driver, SDK or mock binary. Original raw frames are read without rewriting. Saved native history is copied
to the new run, and writable fixtures live in a new workspace. Recorded cwd stays
unchanged where it is part of native protocol or history identity.

For an interpreter, use `--driver /absolute/python --driver-arg /absolute/driver.py`.
Driver arguments are passed directly, without shell evaluation.

## Batch execution

`--cases-file` replaces `--materials` and accepts a JSON array:

```json
[
  {"case": "/private/codex/SDK-US-001/SDK-CASE-001"},
  {"case": "/private/claude/SDK-US-001/SDK-CASE-001"}
]
```

Each item may optionally select a `driver` with `command` and `args`; otherwise
the command-line driver is used. When every item selects a driver, no global
`--driver` is needed. Provider/case identity is read from materials.
`--jobs 4` runs four independent cases. `summary.json` retains every result,
including failures. A non-passing case makes the batch command fail.

## Explicit recording

Only explicit `record` or `protocol record` launches a native process. Ordinary tests and replay do not call it:

```sh
agent-sdk-testify record \
  --provider claude --case SDK-CASE-008 \
  --materials /absolute/repo/tests/simulation/scenarios \
  --driver /absolute/venv/bin/agent-sdk-testify-claude \
  --cli /absolute/claude --config /absolute/selected-claude-config \
  --output /absolute/private/new-capture
```

Go reads case data and provider bindings, but does not interpret JSON workflow
steps. Native drivers own their case code. Shared files and prompts are reused;
provider-specific native inputs stay explicit. `--config` copies only the selected
provider's configuration/credential files, never an entire HOME; inherited auth
and proxy environment values are selected explicitly. Those copies are removed
when execution finishes. Private output is not automatically admitted or published.

Model-change cases 022-024 take `--initial-model A --model B`, with distinct
explicit native models. The plan retains both identities: Claude starts with A,
and Codex selects A at thread creation, before requesting B. A same-value reset
or control acknowledgement alone does not prove an effective model change.

Existing-child case 035 takes `--history-seed /absolute/completed-034-capture`.
The seed must be a passed child-history capture produced by the isolated recording
runner. Go prepares saved native records directly from that completed capture;
no SDK replay is required first. Original parent/child IDs, saved bytes, hashes
and project identity remain unchanged, and the case observes them without new
input. Codex also checks the source project's discovery roots before resuming;
the original cwd is not replaced to conceal personal skill inputs.

New `record` attempts do not copy saved records from `--materials`, and ignore
embedded `historySeed`, `historyProject` and old isolation assertions in loaded
plans. Only explicit `--history-seed` imports saved context. Successful native
setup stamps `recordingIsolation: personal-skills-v1` in the new plan, bound by
the existing capture manifest's plan hash. This is a startup-policy assertion,
not a scan or proof of native skill loading or source authenticity. A legacy
capture without that assertion cannot seed a new recording; do not retrofit the
assertion into old evidence. Existing offline `replay` and `simulate` continue
to accept legacy materials without this assertion and never certify them.

Codex remote-close uses a Go-owned authenticated native service and two Go-owned
recording peers. The driver only performs and asserts the two SDK connections.
All process/CLI paths and driver dependencies must already be installed. Actual
versions and errors are diagnostic information, not exact-version gates.

## Minimal driver convention

Go starts `driver [prefix arguments] --request /absolute/request.json`:

```json
{
  "case": "SDK-CASE-008",
  "materials": "/private/run/materials",
  "workspace": "/private/run/project",
  "peer": {"command": "/absolute/peer-entry", "args": []},
  "output": "/private/run/driver"
}
```

Go supplies HOME, native config and tmp through the environment. Selected native
configuration contributes connection settings and credentials only; inherited
hooks, projects and integrations are excluded from the isolated attempt. New
captures scan selected credentials without rewriting protocol evidence; a match
marks `credentialScan: quarantined` and makes the run fail. `materials/case.json`
contains case data and any provider-specific source inputs. The driver must use
the supplied peer; it must not discover or launch an alternative real Agent.
Remote cases also receive `peer.probe` with the second command/args. Go SDK
child-policy comparisons receive `peer.suppress` with an independent command/args
and receipt. Go prepares their writable material views before launching the driver.
For Go SDK remote simulation, `peer.remote` supplies `url` and `bearerToken` for
the runner-owned offline service; the driver performs SDK calls against that endpoint.
SDKs that accept only an executable path receive a runner-generated launcher.
Materials contain no runtime `_mock` or `_peers` entry.

The private `peer` subcommand is a process-lifetime adapter, not a second control
protocol. It preserves protocol stdio and gives client EOF time to drain on TERM.

The driver writes `output/result.json`:

```json
{"status": "passed", "message": ""}
```

Statuses are `passed`, `failed`, `untriggered`, `unsupported`. Additional result
fields and diagnostic files belong to the driver; Go does not interpret them.
A driver that delivered a result exits 0, including a business failure. Crashes
and startup failures use a nonzero exit code. Logs use stderr, independent of SDK
protocol pipes. Go preserves the result even if process or transport checks fail.
Only a passing driver result and a valid run count as passed.

No additional runtime Go-version check, SDK/CLI allowlist, installation step, or
frozen binary hash is required. `--timeout` optionally bounds the whole command;
zero is unlimited. Version probes share the task cancellation context. Cancellation
reaps owned processes and preserves diagnostics. Recording output is directly
reusable as the next stage's material directory, with a source-data manifest.
Protocol matching and business assertions remain strict.

Without `--output`, attempts persist under `$AGENT_SDK_TESTIFY_DATA_DIR/attempts/<mode>/attempt-*`.
The data root defaults to `$XDG_DATA_HOME/agent-sdk-testify`, or
`~/.local/share/agent-sdk-testify` when XDG_DATA_HOME is unset. Explicit output must
be a new directory whose parent exists; existing attempts are never overwritten.
Tests pass their own temporary output paths. Failures remain for review.
See the [driver handoff contract](../tests/simulation/drivers/README.md#stage-handoff) for the next stage.

## Offline validation

```sh
mise exec -- bash scripts/with-agent-sdk-testify.sh go test ./...
```

Runner tests use a synthetic native process and the real Go record/mock executable.
They check fresh environments, unchanged source data, different tool identities,
driver errors, false pass reports, capture completion and cancellation. Private
reviewed real recordings are indexed by the required corpus gate. See the
[adoption and gate commands](../tests/simulation/drivers/README.md#审核采用和日常门禁).
The Go binary owns `corpus review|qualify|adopt|gate`. `record --index` automatically
runs official replay, qualifies evidence, and replaces the local corpus entry;
`record` without `--index` captures private data only. The repository shell entry
supplies its index and scenario paths. Neither entry commits or pushes Git data.
`corpus gate --sdk-driver /absolute/sdk-driver --index /corpus/index.json
--scenarios /catalog` uses prebuilt binaries without a checkout, compiler, Python,
official SDK, or account. Optional `--source-root` binds checkout source hashes;
the legacy shell gate builds this tool and requires an explicit SDK-owned
`AGENT_SDK_DRIVER_BIN`; it does not build the SDK driver. Coverage also records
binary hashes and available Go build revision metadata.
Missing required data fails CI; synthetic tool tests do not fill that gap.
Actual real-Agent execution is not implied by an offline or synthetic test pass.

Corpus requirements are `required`/`optional`, derived from the shared case
`required` boolean with explicit provider-binding `required` overrides, never from route availability.
Claude cases 017 and 032 are optional; their Codex counterparts remain required.
Case 028 checks native configuration forwarding and correlated response handling, not visual effects. `verification` records
`passed`/`failed`/`unverified`/`blocked`/`unsupported` independently of `adoption`
and the gate's current `simulationStatus`. Non-passing verification needs a reason.
Cases 003, 025 and 026 are optional; 012 remains required even when its official driver
is blocked. Required gaps fail the gate, including planned/unsupported routes.
Successful replay or re-adoption of identical frames cannot clear a known failed
or unverified business goal.

## 内置仿真的 MASS 接入例子（目标接口，尚未实现）

本节约定内置入口的用法，不表示下面的新命令已经可执行。现有
`protocol mock --scenario`、`replay`、`simulate` 仍是当前入口。
材料清单、环境和职责的权威合同见
[内置材料与下游直接使用](../harness/modules/agent-sdk-testify/design.md#内置材料与下游直接使用)。

MASS 继续使用自己的测试和 CLI/HTTP 业务入口，不向本工具提供 driver。
当前 MASS Runtime 配置已有 `executable`、`args`、`envPolicy`、`env`；
Session 创建已有 `--cwd`。示例直接沿用这些接口。
工具只替换原生 Agent 进程，MASS 仍通过其固定版本的真实 Go SDK 通信。

### 1. 选定可兼容场景

拟新增的命令约定如下。工具运行期无需 SDK checkout、编译器、官方 SDK、账号或网络。

```sh
agent-sdk-testify cases list --provider codex
agent-sdk-testify cases show --provider codex --case "$CASE_ID" --json
agent-sdk-testify mock prepare --provider codex --case "$CASE_ID" \
  --output /absolute/new-prepared-run
agent-sdk-testify mock clean --run /absolute/new-prepared-run/run.json
```

`list/show` 只读取随版本发布的集合。`prepare` 校验并释放材料，stdout 返回一个
schemaVersion=1 的 JSON 描述；不会启动 MASS、真实 Agent 或 mock 协议连接。
`clean` 清理运行描述绑定的工具资源并保存清理回执，重复执行安全；仍被使用时失败。
清理前已落盘的准备描述、报告和诊断保留在 output，资源放在其中独立的 resources 子目录。

选择 case 前阅读其完整合同，确认握手、输入、模型、会话选项、cwd、审批和关闭顺序。
下面的完整例子只覆盖：Codex、一个连接、创建 Session 后提交一次文本、
无需审批、无需额外历史导入、最终得到确定的完整 agent 文本，再关闭 Session/Runtime。
`PROMPT` 和 `EXPECTED_TEXT` 必须取自选中 case 的合同，不能随意填“hello”。

MASS 当前 Codex 握手的 ClientInfo 固定为 `{"name":"mass","version":"2"}`，
默认进程参数为 `["app-server"]`。创建 Session 不自动提交输入，也没有任意模型选项的
CLI 配置入口。只有这些实际请求与录制一致的 case 才适用；其他请求字段也继续严格检查。
这不是现有录制已兼容 MASS 的证据。没有合适的已发布 case 时，应报告未执行及缺口，
另行授权取得兼容材料；不得修改原始帧或放宽匹配。

### 2. 环境描述与配置映射

以下是 `prepare` 返回值的形状示例，路径由工具产生，不能在测试里硬编码：

```json
{
  "schemaVersion": 1,
  "run": "/evidence/prepared/run.json",
  "executable": "/opt/bin/agent-sdk-testify",
  "args": ["app-server"],
  "workspace": "/evidence/prepared/resources/workspace",
  "environment": {
    "HOME": "/evidence/prepared/resources/home",
    "TMPDIR": "/evidence/prepared/resources/tmp",
    "CODEX_HOME": "/evidence/prepared/resources/codex",
    "AGENT_SDK_TESTIFY_RUN": "/evidence/prepared/run.json"
  },
  "contract": "/evidence/prepared/contract.json",
  "report": "/evidence/prepared/report.json",
  "cleanupReport": "/evidence/prepared/cleanup.json"
}
```

`AGENT_SDK_TESTIFY_RUN` 是本次准备的唯一选择入口。SDK 将 executable 当原生 Agent
启动并追加受支持的原生参数时，工具通过这个环境变量识别本次 mock；
参数或场景选择冲突直接失败。它不与外部 `scenario` 或其他 case 选择混用。
`args` 是该 provider 的原生入口参数，不是传给下游的 `mock` 子命令参数。
新环境不得继承真实凭证；若需要 PATH 等变量，由准备描述显式提供。

MASS Runtime 配置映射为：

```json
{
  "executable": "<prepared.executable>",
  "args": ["<prepared.args 中的各个参数>"],
  "envPolicy": "replace",
  "env": "<prepared.environment 对象>"
}
```

实际值由 JSON 序列化生成。不要填写 `scenario`；它在 MASS 当前合同中表示外部合成
文件路径，不是内置 case ID。Session 的 cwd 使用 `prepared.workspace`。
对于协议身份限制下无法由 MASS 使用该 workspace 的 case，prepare 必须明确拒绝
此使用方式，而不是返回一个看似可用、实际无法匹配的配置。

环境必须同时作用于 MASS 服务进程和原生 mock 子进程：前者覆盖 SDK 宿主内的历史读取，
后者覆盖协议对端。给子进程配置 HOME 不能改变已运行服务的 HOME。
因此不要复用已连接个人目录的常驻 massd，先 prepare，再启动独立 massd。

### 3. MASS 自己运行完整测试

下面是下游测试脚本的完整示例，使用 Python 标准库调用 MASS CLI；
它不实现 `driver --request`，也不会被 testify 运行器启动。
示例按上述目标接口编写；新增命令及真实材料接入实现后才能执行。
参数是工具、massd、massctl 的绝对路径，以及所选 case 的 ID、原始 prompt 和预期完整文本。
MASS 可先在自己的仓库完成构建；构建不是 mock 运行时依赖。
若下游将该示例放在 `mass_builtin_test.py`，调用形式为：

```sh
python3 mass_builtin_test.py /opt/bin/agent-sdk-testify \
  /absolute/mass/bin/massd /absolute/mass/bin/massctl \
  "$CASE_ID" "$PROMPT" "$EXPECTED_TEXT"
```

```python
import json
import pathlib
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse

tool, massd, massctl, case_id, prompt, expected = sys.argv[1:]
for executable in (tool, massd, massctl):
    if not pathlib.Path(executable).is_absolute():
        raise ValueError("tool, massd and massctl must be absolute paths")
evidence = pathlib.Path(tempfile.mkdtemp(prefix="mass-simulation-evidence-"))
print("evidence:", evidence, flush=True)
daemon = None
prepared = None
runtime_started = False
session_open = False
url = None
errors = []

def run(argv, env=None):
    result = subprocess.run(
        argv, env=env, text=True, capture_output=True, timeout=20,
    )
    with (evidence / "commands.jsonl").open("a") as log:
        log.write(json.dumps({
            "argv": argv, "exitCode": result.returncode,
            "stdout": result.stdout, "stderr": result.stderr,
        }) + "\n")
    result.check_returncode()
    return json.loads(result.stdout) if result.stdout.strip() else None

def ctl(*args):
    return run(
        [massctl, "--url", url, "-o", "json", *args],
        env=prepared["environment"],
    )

def history():
    events, cursor = [], ""
    while True:
        args = ["session", "history", "sample", "-n", "simulation",
                "--limit", "100"]
        if cursor:
            args += ["--after", cursor]
        page = ctl(*args)
        events.extend(page["events"])
        cursor = page.get("next", "")
        if not cursor:
            return events

def check(condition, message):
    if not condition:
        raise AssertionError(message)

try:
    contract = run([tool, "cases", "show", "--provider", "codex",
                    "--case", case_id, "--json"])
    (evidence / "case.json").write_text(json.dumps(contract, indent=2))
    prepared = run([tool, "mock", "prepare", "--provider", "codex",
                    "--case", case_id, "--output", str(evidence / "prepared")])
    (evidence / "prepared.json").write_text(json.dumps(prepared, indent=2))
    check(prepared["schemaVersion"] == 1, "unsupported prepare schema")
    config = {
        "executable": prepared["executable"],
        "args": prepared["args"],
        "envPolicy": "replace",
        "env": prepared["environment"],
    }
    # Start the SDK host only after the tool has prepared its environment.
    with (evidence / "ready.json").open("w") as out, \
         (evidence / "massd.stderr").open("w") as err:
        daemon = subprocess.Popen(
            [massd, "--local", "--listen", "127.0.0.1:0",
             "--data", str(evidence / "mass-data")],
            env=prepared["environment"], stdout=out, stderr=err,
        )
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        check(daemon.poll() is None, "massd exited before readiness")
        try:
            url = json.loads((evidence / "ready.json").read_text())["url"]
            break
        except json.JSONDecodeError:
            time.sleep(0.05)
    check(url is not None, "massd readiness timeout")

    # runtime start constructs the real Codex SDK and launches the mock.
    ctl("runtime", "create", "sim", "--type", "codex",
        "--config-json", json.dumps(config))
    ctl("runtime", "start", "sim")
    runtime_started = True
    created = ctl("session", "create", "sample", "-n", "simulation",
                  "--runtime", "sim", "--cwd", prepared["workspace"])
    session_open = True
    check(bool(created["status"]["sessionId"]), "missing native session identity")
    receipt = ctl("session", "prompt", "sample", "-n", "simulation",
                  "--text", prompt)
    (evidence / "input-receipt.json").write_text(json.dumps(receipt, indent=2))
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        current = ctl("session", "get", "sample", "-n", "simulation")
        saved = history()
        messages = [
            "".join(b["text"] for b in event["payload"]["content"]
                    if b["type"] == "text")
            for event in saved if event["type"] == "agent.message"
        ]
        if current["status"].get("state") == "idle" and expected in messages:
            break
        time.sleep(0.05)
    else:
        raise AssertionError("missing expected complete message and idle state")
    (evidence / "history.json").write_text(json.dumps(saved, indent=2))
    # A completed turn is not EOF. Close Session, then the shared Codex client.
    ctl("session", "archive", "sample", "-n", "simulation")
    session_open = False
    ctl("runtime", "stop", "sim")
    runtime_started = False
    report = json.loads(pathlib.Path(prepared["report"]).read_text())
    check(report["status"] == "passed", "mock run failed")
    check(bool(report["connections"]), "missing connection receipts")
    for peer in report["connections"]:
        check(all(peer[key] is True for key in (
            "matched", "fullyConsumed", "clientEOF",
            "processExited", "cleanupCompleted",
        )), "incomplete mock connection")
except BaseException as error:
    errors.append(str(error))
finally:
    # Failure cleanup does not turn interrupted or incomplete replay into success.
    if daemon is not None:
        if daemon.poll() is None:
            if url and session_open:
                try:
                    ctl("session", "cancel", "sample", "-n", "simulation")
                    ctl("session", "archive", "sample", "-n", "simulation")
                except Exception as error:
                    errors.append("session cleanup: " + str(error))
            if url and runtime_started:
                try:
                    ctl("runtime", "stop", "sim")
                except Exception as error:
                    errors.append("runtime cleanup: " + str(error))
            daemon.terminate()
            try:
                daemon.wait(timeout=15)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait(timeout=5)
                errors.append("massd required forced termination")
        if daemon.returncode != 0:
            errors.append("massd exit code: " + str(daemon.returncode))
        if url:
            address = urllib.parse.urlsplit(url)
            try:
                with socket.create_connection(
                    (address.hostname, address.port), timeout=0.5,
                ):
                    errors.append("owned listener is still reachable")
            except ConnectionRefusedError:
                pass
            except OSError as error:
                errors.append("listener release unconfirmed: " + str(error))
    if prepared is not None:
        try:
            run([tool, "mock", "clean", "--run", prepared["run"]])
            cleanup = json.loads(pathlib.Path(prepared["cleanupReport"]).read_text())
            check(cleanup["status"] == "passed", "tool cleanup failed")
            check(not pathlib.Path(prepared["workspace"]).exists(),
                  "tool workspace still exists")
        except Exception as error:
            errors.append("tool cleanup: " + str(error))
    result = {"status": "failed" if errors else "passed", "errors": errors}
    (evidence / "test-result.json").write_text(json.dumps(result, indent=2))
if errors:
    raise SystemExit("\n".join(errors))
print("PASS: MASS business assertions, strict replay and resource cleanup")
```

测试脚本故意保留 MASS 的数据目录和所有报告，供历史／失败诊断使用；
它关闭服务并检查端口释放，不自动删除这些证据。工具的临时 HOME、workspace、
config 和 tmp 由 `mock clean` 删除，报告保留。CI 产物保留期限由 MASS 自己管理。
脚本没有开启自动审批；出现不属于所选单轮合同的交互应失败，不能自动回答来凑齐回放。
本例通过持久历史检查完整消息，未订阅 SSE；如需验证实时事件，
必须先启动 watch 并等待 `subscription.ready`，测试后单独关闭观察者。

### 4. Claude 与历史类测试的差别

Claude 使用 `--provider claude` 准备，得到 CLAUDE_CONFIG_DIR 和相应 HOME，
Runtime type 改为 `claude`，`args` 必须为空；工具接受 SDK 追加的受支持原生参数。
进程通常在创建 Session 时启动，关闭 Session 时结束，不沿用 Codex 的共享连接假设。
Claude 未确认原生关联时可能为 unknown，不能简单复制上例的 idle 成功条件。
应按该 case 合同断言其原生完成与 MASS 状态，不能把 unknown 强行当作成功。

历史类 case 在启动 massd 前由工具释放种子并保存 materialization 关联；
下游通过 MASS 的 session load/resume 等公共接口操作，不能直接向 MASS 事件库造历史。
需要多个连接时，每个连接都有预先声明的入口与独立回执；本单连接例子不覆盖该用法。
只有这些路径实现并验证后，相应 case 才能列为可用。
