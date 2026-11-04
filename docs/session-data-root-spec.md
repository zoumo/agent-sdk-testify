# 自包含 session 实现规格

## Problem Statement

现有实现仍保留外部材料路径、action 级材料选择、managed mock 入口和基于初始
source 类型的动作限制，不能保证 prepare 后独立使用 session，也不能保证所有动作
使用同一 current recording。finish 的忙碌处理和关闭时机也需要与目标设计对齐。

## Solution

以[工具设计](design.md)为唯一目标合同，将 session 作为唯一业务数据根。
本规格限定实现范围和验收方式，不另行定义材料格式、生命周期或可信等级。
本次使用本地 ticket，不发布 GitHub Issues；此前 prepared-session-lifecycle
任务中与当前设计冲突的 alias、action override 和 capture handoff 合同不再适用。

## User Stories

1. 作为消费者，我希望 prepare 完整复制输入，以便删除原始输入后仍能使用 session。
2. 作为维护者，我希望 case 没有内置材料时仍能 prepare，以便随后显式录制。
3. 作为消费者，我希望初始 source 只决定初始状态，以便按真实前提调用各个动作。
4. 作为录制者，我希望多次 record 使用固定录制来源，以便不会因 current 切换改变录制目标。
5. 作为录制者，我希望只有成功录制才替换 current，以便失败尝试不会破坏已有材料。
6. 作为审核者，我希望旧验证证据保留且不被继承，以便结论始终对应精确材料。
7. 作为维护者，我希望 verify 只验证 current，以便验证输入明确且可追溯。
8. 作为维护者，我希望默认验证不写仓库，以便采用必须显式发生。
9. 作为消费者，我希望未验证材料也能 mock，以便自行评价消费者业务结果。
10. 作为 CLI 用户，我希望只有 prepare 接受 source，以便不再面对多套材料选择入口。
11. 作为消费者，我希望完整导出 current，以便在另一个 session 重用同一材料包。
12. 作为消费者，我希望 export 不改变会话或可信等级，以便保存材料不会伪装成验证。
13. 作为消费者，我希望忙碌时 finish 不修改数据，以便不会中断正在执行的动作。
14. 作为消费者，我希望关闭后仍能导出并重试清理，以便保留证据而无需重新打开会话。

## Implementation Decisions

- prepare、record source、current recording、execution 和 material 的合同遵循
  [生命周期](design.md#会话生命周期)、[数据边界](design.md#命令与数据边界)及
  [数据所有权](design.md#数据布局与所有权)。
- record、verify、mock、export、finish 的执行条件和失败行为遵循
  [动作合同](design.md#记录验证与回放)和[失败与证据](design.md#失败与证据)。
- 官方身份、验证与显式采用遵循[信任与入仓](design.md#信任与入仓)。
- 新设计替代不兼容的旧 CLI 入口和会话数据合同；拒绝不兼容数据，不能静默按新结构解释。
- 复用已有协议、历史、官方执行和材料能力；不另建一套 action 生命周期。
- 同步现有 CLI 说明、术语和开发规范中的必要引用，保持每项目标规则只有一个维护位置。

## Testing Decisions

用户已确认以 installed CLI 为统一纵向验收接缝。通过 argv、stdio、退出码、
descriptor、文件与哈希检查外部可观察行为，不以内部函数调用替代验收。
沿用现有 conformance suite、受控进程、synthetic 材料和 driver substitute。

覆盖[设计要求的测试边界](design.md#测试边界)，并特别验证原路径删除后的独立性、
record 成功/失败的 current 切换、验证不继承、consumer mock、导出再导入、
忙碌 finish 无修改，以及 closed 后导出和清理重试。
每张 ticket 执行对应检查，最终执行开发规范要求的完整离线门禁。

## Out of Scope

- 不启动真实 Agent、不新增真实录制、不安装官方 SDK，不执行网络验证。
- 不修改消费者 SDK、MASS 或产品 E2E 的业务编排。
- 不放宽严格回放，不改变 case 身份、required 集合或材料入仓政策。
- 不发布远端 issue，不提交或推送代码，不改写用户已有修改。

## Further Notes

实现按本地 ticket 的依赖顺序推进，每张 ticket 使用独立 Codex thread。
主上下文负责编排、验收结果汇总和依赖推进，不直接实现 ticket。
