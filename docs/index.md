---
okf_version: "0.3"
title: Testify 文档
description: 面向 SDK 测试使用者和 Testify 维护者的录制、回放、共同验收与开发文档。
license: Apache-2.0
---

# Testify 文档

## 录制与回放

* [从 SDK 测试使用 mock](using-mock.md) - 说明如何从 SDK 测试准备回放材料、启动 mock、检查 execution 并清理 fixture。
* [录制并回放消费者的 SDK 交互](consumer-recording.md) - 说明如何用 consumer plan 准备 fixture、通过 SDK 录制交互、回放 current recording 并释放资源。
* [Go 库回放合同](library-entry-spec.md) - 定义 Go 进程内回放的流归属、启动与关闭顺序、执行报告和通过条件。

## 设计与维护

* [工具设计](design.md) - 定义 Testify 的 CLI 与 Go 库职责、fixture 生命周期、材料所有权和录制验收条件。
* [SDK 共同验收](cases.md) - 定义 SDK 用户故事、可录制回放的 case、流程语义和验收边界。
* [开发规范](development.md) - 规定 Testify 的模块依赖、代码布局、录制材料与测试边界，以及构建和发布要求。
