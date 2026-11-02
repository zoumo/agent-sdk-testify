---
type: Development Standard
title: agent-sdk-testify 开发规范
status: stable
---

# Development standards

## Module 与依赖

使用单一 module `github.com/zoumo/agent-sdk-testify` 和单一工具二进制，
遵循 mise.toml 固定的编译器与依赖版本；不添加嵌套 module、replace 或 go.work。
生产代码及默认测试不得导入 agent-sdk-go、Go driver 或 SDK fixture 容器。
官方 SDK 仅作为 record/verify 的显式环境依赖，不在运行时自动安装。

工具拥有 case、provider binding、官方 driver、材料、索引、协议匹配和自有环境。
Go SDK 拥有 runtime、Go driver、simulate、业务断言、并行门禁和 SDK 覆盖报告。
消费者通过 mock 进程、场景/运行描述与独立回执接入，不通过跨仓库 Go 库。
公开业务入口限定为 record、verify、mock；材料资格、归档、采用和协议桥接为内部能力。

<a id="layout-code-layout-standards"></a>

## Code layout standards

| 位置 | 归属 |
|---|---|
| 仓库根目录 | 单二进制和环境/进程实现 |
| `protocol/` | 协议处理及 capture、command、rawjson、recording、replay |
| `tests/simulation/drivers/` | 官方 driver、Python wheel、离线 driver 测试和文档 |
| `tests/simulation/scenarios/` | 权威 story/case、输入、预期、provider binding |
| `tests/simulation/recordings/` | 已跟踪材料、固定索引、来源与哈希 |
| `migration/agent-sdk-go/` | SDK 专属测试的待迁移原件；SDK 拥有，不参与默认工具测试 |

目标布局可以按工具设计迁移；移动时同步 imports、embeds、脚本、测试及文档。
不要添加无需求的 cmd/pkg、框架、转发层、动态插件或通用流程 DSL。
协议包不依赖 case 编号、driver 或 SDK。capture 拥有自己的 I/O、选项和类型化错误，
只在命令边界映射退出码。provider 的配置、启动参数和历史解释留在 provider 文件。
测试使用与消费者相同的进程接口，不导入被测 SDK 来间接证明工具正确性。

<a id="layout-simulation-data-and-integration-fixtures"></a>

## Simulation data and integration fixtures

原件及失败尝试保存在 Git 外私有归档。已跟踪材料按 provider/story/case/attempt 归属；
固定索引保留所有要求、失败和缺口。移动不改变原始 plan/raw、attempt 身份或来源哈希。
历史运行副本须保留原/派生路径及双向哈希关联，不改写原件或放宽协议字段。
材料发布须遵守[工具合同](modules/agent-sdk-testify/design.md#story-数据格式与存储)。
合成输入只用于专项测试，不能登记为真实录制或用户故事成功证据。

## 编码约定

使用清楚的名称和语言惯用写法；注释解释原因，避免重复代码。
复用解决实际重复，性能优化需要实际需求。第三方依赖须有明确用途、版本与来源。
限制 lint 例外并说明原因；lint 不替代编译、测试和 race。
协议 stdout 与诊断分离，不记录认证秘密；可复用模块不修改全局日志或遥测配置。
测试及资源隔离遵循[测试规范](testing.md)。
