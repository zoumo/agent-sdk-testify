---
okf_version: "0.3"
title: Testify 文档
description: 面向 SDK 测试使用者和 Testify 维护者的录制、回放、共同验收与开发文档。
license: Apache-2.0
---

# Testify 文档

## 录制与回放

- [从 SDK 测试使用 mock](using-mock.md) — 准备回放材料、启动 mock 并检查结果。
- [录制并回放消费者的 SDK 交互](consumer-recording.md) — 通过 consumer plan 录制并复用交互。
- [Go 库回放合同](library-entry-spec.md) — 查阅进程内回放的调用顺序、资源归属和通过条件。

## 设计与维护

- [工具设计](design.md) — 查阅 CLI 职责、fixture 生命周期和官方编排边界。
- [SDK 共同验收](cases.md) — 查阅用户故事、case 说明和验收边界。
- [开发规范](development.md) — 查阅依赖、构建、离线测试和发布要求。
