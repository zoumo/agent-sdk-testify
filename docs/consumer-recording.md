---
type: Guide
title: 录制并回放消费者的 SDK 交互
description: "说明如何用 consumer plan 准备 fixture、通过 SDK 录制交互、回放 current recording 并释放资源。"
tags: [recording, replay, fixture]
sources:
  - id: tool-design
    resource: "design.md"
    title: "录制与回放合同"
---

<a id="消费者录制"></a>

# 录制并回放消费者的 SDK 交互

用自己的 SDK 启动 `agent record`，采集与原生 Agent 的交互。采集成功后，用同一 fixture 的 current recording 回归测试。消费者负责 SDK 宿主、业务断言和 fixture 清理。Testify 负责隔离环境、协议采集和严格回放。

<a id="边界"></a>
<a id="生命周期"></a>
<a id="consumer-plan"></a>
<a id="prepare"></a>

## 用 consumer plan 准备 fixture

前置条件如下。

- `PATH` 包含 `agent-sdk-testify`。
- fixture 路径是尚不存在的绝对目录，父目录已存在。
- 本地录制使用可运行的 Codex 或 Claude CLI。远程录制使用已有的认证 WebSocket 服务。

1. 创建 `/absolute/consumer-plan.json`。将 `project` 填为客户端发给 Agent 的原始协议项目路径。`caseId` 由消费者指定。

	```json
	{
		"version": 1,
		"agentType": "codex",
		"caseId": "consumer-two-turns",
		"driverProfile": "consumer",
		"project": "/absolute/original-project"
	}
	```

2. 创建 fixture。如果测试使用已有会话，向 `prepare` 传入 `--history-seed` 和完整历史种子目录。

	```sh
	agent-sdk-testify prepare --agent codex \
		--plan /absolute/consumer-plan.json \
		--fixture-dir /absolute/fixture
	```

`prepare` 生成隔离环境和 record route。此时可以开始配置 SDK 宿主。[Consumer plan 字段](design.md#consumer-plan)、[历史种子与派生规则](design.md#材料计划与运行配置)

<a id="配置发现"></a>
<a id="agent-record"></a>
<a id="原生启动参数"></a>

## 通过 SDK 录制

在同一 fixture 上串行运行独立的 SDK 使用过程。从读取 route 开始协调，直到所有进程退出、输出结算且 route 恢复。若需要独立并行，为每次使用创建一个 fixture。[宿主的协调区间](design.md#sdk-宿主的运行时入口)

1. 读取 `/absolute/fixture/runtime/routes/record.json`。使用 Go 库时，从 `Prepare` 或 `Open` 的返回值取得 route。[SDK 宿主的运行时入口](design.md#sdk-宿主的运行时入口)
2. 核对 route 身份和运行目录。将 route 的 `env` 应用到 SDK 宿主，将宿主工作目录设为 `workspace`。录制配置与认证来自允许的原生配置和 `runtimeEnv` 列出的变量。[环境与隔离](design.md#环境与隔离)
3. 将 SDK 启动 Agent 的命令设为对应的 `agent record` 调用。两个命令都使用 SDK 的原生 stdio 协议来传输客户端帧。

   - **本地 CLI**。提供真实 Agent 的绝对可执行路径，并将原生 stdio 参数放在 `--` 后。使用 CLI route 时，`connection.args` 已含 `--cmd`，在其后追加可执行路径、`--` 与原生参数。Testify 原样转发这些参数，生成的证据字段省略它们。[原生启动参数](design.md#原生启动参数)

	 ```sh
	 agent-sdk-testify agent record --fixture-dir /absolute/fixture \
		 --cmd /absolute/codex -- app-server --listen stdio://
	 ```

   - **已有 WebSocket 服务**。使用 `--url` 和 `--token-file`，省略本地 CLI 参数。token 文件使用私有普通文件的绝对路径，例如权限为 `0600` 的文件。将文件放在 fixture 外或 fixture 的 `runtime/` 内。Testify 按 stdio 和 WebSocket 双向传输，结束时关闭自身的连接。你负责服务的生命周期。[远程录制与命令边界](design.md#远程录制与命令边界)

	 ```sh
	 agent-sdk-testify agent record --fixture-dir /absolute/fixture \
		 --url ws://127.0.0.1:8765/agent \
		 --token-file /private/runtime/bearer-token --timeout 2m
	 ```

4. 执行 SDK 业务操作。完成后，关闭 SDK 写往 `agent record` 的协议流。继续读取录制代理的输出，直到 EOF。等待 SDK 宿主和录制进程退出。
5. 检查业务断言和本次采集结果。采集通过后，Testify 封存不可变 material，并更新 current recording。若交付或清理失败，读回持久结果中的 `publicationCommitted`，确认 current 是否已更新。[发布提交点与失败分支](design.md#封存发布与恢复)

<a id="协议执行结果"></a>

### 读取本次采集结果

在启动录制进程时，可用可写管道或文件描述符接收 `--result-fd` 通知。描述符值至少为 `3`。例如，将录制进程的描述符 `3` 重定向到结果文件。

```sh
agent-sdk-testify agent record --fixture-dir /absolute/fixture \
	--result-fd 3 --cmd /absolute/codex -- app-server --listen stdio:// \
	3>/absolute/record-result.jsonl
```

通知用 JSONL 写入独立描述符。使用通知中的 `materialId`、`executionId`、`connectionId` 定位本次结果。采集和业务结果分别核对。[CLI 结果与通知](design.md#cli-结果与通知)、[采集通过条件](design.md#agent-record)

<a id="agent-mock"></a>
<a id="使用-mock"></a>
<a id="多次使用"></a>
<a id="可重复性"></a>

## 回放 current recording

1. 读取同一 fixture 的 `runtime/routes/mock.json`，使用 mock route 配置 SDK。让 SDK 启动 `agent mock`。完整步骤见 mock 指南。[配置 SDK 宿主](using-mock.md#sdk-宿主环境)
2. 用相同输入运行 SDK 操作。为每次运行生成的 UUID 和业务字段提供确定性测试值。回放仅按原生协议规则重绑定关联 ID。[严格回放](design.md#严格回放)
3. 检查业务断言及回放结果。建立消费者的回归基线时，连续回放 2 次，并核对每次都严格匹配。
4. 如果要在另一 fixture 使用材料，在下一次录制前导出 current recording。用 `prepare --recording` 将导出包导入新 fixture。[导出与导入](using-mock.md#导出与复用)

同一 consumer plan 可在同一 fixture 中重复录制。每次成功录制生成独立 package。每次录制和回放均使用独立的可写 overlay。[多个 action execution](design.md#多个-action-execution)

## 释放 fixture

1. 关闭 SDK，等待全部进程退出并完成结果结算。
2. 若要保留当前材料，先导出到 fixture 外。若要保留原 package 与执行证据，只删除 `runtime/`。
3. 若无需保留 fixture 内的数据，删除 fixture 根目录。[目录所有权](design.md#fixture-目录所有权)

<a id="fixture-与-execution-descriptor"></a>
<a id="材料布局"></a>
<a id="状态与历史"></a>
<a id="证据与发布"></a>
<a id="安全与失败"></a>
<a id="实现边界"></a>
<a id="非目标"></a>

## 查阅录制合同

消费者保存自己的材料、业务证据和发布结果。`stderr.raw` 可能包含提示词、认证错误或环境信息。默认私下保管录制材料。
consumer material 保留消费者来源。官方材料需通过官方 case 单独录制，并经官方验证和采用。

| 查阅内容 | 文档 |
| --- | --- |
| fixture 和 execution 的字段 | [数据布局与所有权](design.md#数据布局与所有权)、[schema 版本](design.md#schema-版本与兼容) |
| 历史种子、派生文件和哈希 | [材料计划与运行配置](design.md#材料计划与运行配置) |
| 本地 CLI、WebSocket 与隔离配置 | [远程录制](design.md#远程录制与命令边界)、[环境与隔离](design.md#环境与隔离) |
| 失败后的 current 与结果 | [封存、发布与恢复](design.md#封存发布与恢复) |
| 录制来源、隐私和采用要求 | [信任与入仓](design.md#信任与入仓) |
