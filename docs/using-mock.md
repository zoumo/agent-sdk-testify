---
type: Guide
title: 从 SDK 测试使用 mock
description: "说明如何从 SDK 测试准备回放材料、启动 mock、检查 execution 并清理 fixture。"
tags: [replay, fixture, testing]
sources:
  - id: document
    resource: "using-mock.md"
    title: "从 SDK 测试使用 mock"
  - id: tool-design
    resource: "design.md"
    title: "Testify 工具设计"
---

# 从 SDK 测试使用 mock

用 `agent mock` 在 SDK 测试中回放 current recording。SDK 发送原生协议请求。Testify 严格匹配请求并回放帧，你在 SDK 测试中检查业务结果。使用进程内 Go 流时，按 Go 库入口接入。[Go 库入口](../README.md#go-库入口)

## 准备材料与 fixture

前置条件是 `PATH` 包含 `agent-sdk-testify`，fixture 使用尚不存在的绝对路径，且父目录已存在。已有录制须位于目标 fixture 外。内置材料随二进制分发。

1. 选择内置 case 或已有 recording。两个命令都准备后续步骤使用的 `/reports/replay-fixture`，运行其中一个。

	```sh
	agent-sdk-testify prepare --agent codex --case SDK-CASE-001 \
		--fixture-dir /reports/replay-fixture
	```

	```sh
	agent-sdk-testify prepare --recording /captures/attempt \
		--fixture-dir /reports/replay-fixture
	```

2. 确认 `prepare` 成功。Testify 复制并校验材料，随后将材料选为 current recording。缺少材料的 case 仍能创建 fixture，回放需要已有 current recording。[固定索引](../data/index.json)、[内置快照](../data/builtin/manifest.json)、[导入边界](design.md#导入边界)

## SDK 宿主环境

每个 fixture 同一时间只运行一次独立的 SDK 测试。使用同一 fixture 时，从读取 route 起持有调用方自己的协调，直到所有 SDK 宿主和 mock 退出、结果结算、route 恢复。独立测试需要并行时，为每个测试准备不同 fixture。[宿主的协调区间](design.md#sdk-宿主的运行时入口)

1. 在启动 SDK 宿主前，读取 `/reports/replay-fixture/runtime/routes/mock.json`。
2. 核对 route 的 `fixtureId`、`action`、`materialId` 和运行目录。发现快照过期或缺失时，停止本次启动。
3. 将 `env` 应用到 SDK 宿主，历史 API 也使用这个环境。用 `workspace` 设置宿主工作目录。Codex 使用 `protocolCwd` 配置协议项目。Claude 使用 `workspace` 配置 `WorkDir` 和历史查询。

`fixture.json` 是 Testify 持有的只读文件。CLI 的 `--fixture-dir` 接收 fixture 目录。SDK 宿主使用 route 提供的路径和环境。[运行时字段](design.md#sdk-宿主的运行时入口)

## 启动连接

1. 将 SDK 的 Agent 启动命令设为 `connection.command` 和 `connection.args`。如果 SDK 只接受可执行文件，设置为 `connection.launcher`。单连接的默认 ID 为 `main`，命令形式如下。

	```sh
	agent-sdk-testify agent mock --fixture-dir /reports/replay-fixture
	```

2. 启动 SDK，运行测试中的业务操作。若 SDK 会探测 Agent 版本，在测试配置中处理探测。Testify 的 `--version` 报告工具版本。
3. 对于声明多个连接的材料，给每个 SDK 客户端配置自己的连接。每条连接在同一 execution 中启动一次。材料声明 `probe` 时，在该客户端使用 `--connection probe`。[连接与 execution](design.md#多个-action-execution)
4. 完成业务调用后，关闭 SDK 发往 mock 的协议输入并继续读出站协议帧。若 SDK 需要获知何时半关闭，使用 `--progress-fd` 提供的完整消费通知。等待 SDK 和 mock 退出。[严格回放与 EOF](design.md#严格回放)

launcher 只转发白名单内的原生启动参数。传入 Testify 的动作选项时，直接使用 `connection.command` 和 `connection.args`。[CLI 结果与通知](design.md#cli-结果与通知)

## 持久结果

1. 检查 SDK 的业务断言。
2. 检查 mock 进程的退出码和本次连接回执。通过时，Testify 完成严格匹配、完整消费、客户端 EOF 和自身资源清理。
3. 若 execution 有多条连接，检查全部连接的回执和 execution 汇总状态。汇总 `passed` 表示全部声明连接通过。

用本次启动的连接回执核对结果。若测试通过 `--result-fd` 接收通知，用通知的 `materialId`、`executionId` 和 `connectionId` 定位持久结果。[结果文件与引用规则](design.md#cli-结果与通知)、[持久数据布局](design.md#数据布局与所有权)

握手或请求与录制不符时，按失败回执定位差异。例如 MASS 客户端的 `mass/2` 握手与官方客户端录制不一致时，mock 报告匹配失败。测试保留录制中的协议帧和来源字节。[严格回放](design.md#严格回放)

## 导出与复用

1. 如果要跨 fixture 保留当前材料，在下一次 `agent record` 或删除 fixture 前执行 `export`。导出目录必须位于 fixture 外，父目录必须存在，目标目录必须尚不存在。

	```sh
	agent-sdk-testify export --fixture-dir /reports/replay-fixture \
		--output /captures/exported-recording
	```

2. 导入新 fixture 后，使用新 fixture 的 route 运行 SDK 测试。

	```sh
	agent-sdk-testify prepare --recording /captures/exported-recording \
		--fixture-dir /reports/imported-fixture
	```

导出目录包含当前 material core。执行回执和官方验证结果保留在原 fixture。[导出合同](design.md#export)

## 释放 fixture

1. 关闭 SDK 宿主，等待全部 mock 进程退出并完成结果结算。
2. 如需保留材料，先导出 current recording。需要保留原 package 和执行证据时，只删除 `runtime/`。
3. 如无需保留 fixture 内的数据，删除 fixture 根目录。[目录所有权](design.md#fixture-目录所有权)
