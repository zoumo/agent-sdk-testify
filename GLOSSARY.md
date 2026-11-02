# agent-sdk-testify 术语

本表定义本工具、录制材料和 mock 消费者共用的核心术语。相同名称只表示同一概念。

## Case 与协议

**User story（用户故事）**:
一个完整的 SDK 使用目标，由若干可独立录制和验证的 case 共同验收。

**Validation case（验证 case）**:
归属于一个用户故事、具有独立身份和输入顺序的完整使用流程。
_Avoid_: 单条断言、专项故障测试

**Provider binding（provider 绑定）**:
某个 provider 对 case 的原生参数、执行路径、required/optional 和限制。

**Connection（连接）**:
一次录制中的独立原生协议通道，例如 `main` 或远端流程的 `probe`。

**Strict replay（严格回放）**:
按录制顺序校验客户端和服务端原生帧，要求关联 ID、未知字段、完整消费和 EOF 一致。

**Receipt（回执）**:
一个连接或进程的持久结果，记录协议状态、退出、清理和失败诊断。

## 命令

**record**:
显式启动真实 Agent，通过官方 SDK 采集一个新的原生交互 attempt。

**verify**:
使用官方 SDK 离线执行已有录制，并校验业务断言、协议匹配、完整消费、EOF 和清理。

**mock**:
用已有录制替代真实 Agent，作为消费者测试使用的原生协议对端。

**Consumer（消费者）**:
通过 `mock` 的进程接口运行自身 SDK 或产品测试的一方。业务断言和测试编排归消费者。

## 材料与发布

**Capture attempt（采集尝试）**:
某个 provider 对一个 case 的一次实际录制，包含 plan、raw、业务报告和收尾证据。

**Material（材料）**:
一次经过审核采用的 capture attempt，可作为 mock 的输入。

**Adoption（采用）**:
将验证通过且有来源证据的尝试写入固定录制库的内部动作。
状态为 `admitted` 时，不代表内容已获公开发布授权。

**Fixed index（固定索引）**:
`recordings/index.json`，列出每个 provider/case 的材料、采用状态、验证结果和缺口。

**Builtin snapshot（内置快照）**:
从固定索引选择 `admitted` 且 `verification=passed` 的材料，压入二进制并由 manifest 哈希绑定。

**Qualification（资格）**:
材料是否具备来源、隔离、官方业务和完整性证据的内部判断。

**Simulation（仿真）**:
消费者用 `mock` 回放录制并执行自身业务测试的过程。仿真结果由消费者报告，工具只报告协议是否完成。

**Synthetic material（合成材料）**:
人工构造、仅用于协议或错误路径测试的输入，不是录制证据，也不计入用户故事验收。

**Evidence level（证据层）**:
真实录制、官方 SDK 验证、SDK 仿真和产品 E2E 等独立结果层级；不同层级不能互相替代。
