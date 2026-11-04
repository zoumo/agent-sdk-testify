# 自包含 session 与分层命令实现规格

## Problem Statement

消费者需要通过稳定的 Agent 协议进程采集或回放，官方录制与验证则需要驱动 SDK
完成业务流程。如果协议命令同时编排 driver，官方 driver 就不能复用消费者的调用
路径，还会产生递归调用、重复生命周期和难以关联的业务证据。会话必须完整拥有输入
与执行证据，材料切换不能破坏已有事实，收尾不能清理仍在使用的环境。

## Solution

以[工具设计](design.md)为唯一目标合同，将 session 作为唯一业务数据根。
公开命令分为会话管理、agent 协议执行和 official 官方编排。官方 driver 与消费者
通过同一组 agent 命令工作；官方编排保存业务事实并关联实际 execution，不创建
自己的协议 execution。本规格限定实现范围与验收，不重复定义生命周期或可信等级。

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
15. 作为消费者，我希望 SDK 启动 agent record 或 agent mock，以便录制与回放使用对称的协议接口。
16. 作为维护者，我希望 official record 驱动官方 SDK 调用公开 agent record，以便官方录制无需内部采集入口。
17. 作为维护者，我希望 official verify 驱动官方 SDK 调用公开 agent mock，以便验证覆盖消费者真实使用的回放路径。
18. 作为录制者，我希望采集成功与业务成功分别记录，以便 driver 后续失败不篡改已发布材料。
19. 作为审核者，我希望业务报告精确关联 execution 和 material，以便其他运行的成功证据不能冒充本次结果。
20. 作为远程录制者，我希望 agent record 连接已有服务并采集双向帧，以便无需独立 connect 命令。
21. 作为远程服务使用者，我希望结束采集只关闭工具自己的连接，以便不终止我的已有服务。
22. 作为维护者，我希望官方编排不阻塞其 agent 子进程，以便等待 driver 时不会发生会话锁死锁。
23. 作为消费者，我希望 driver 启动前和结束前的间隙也禁止 finish，以便宿主环境不会被提前清理。
24. 作为维护者，我希望整个协议实现仅供仓库内部使用，以便跨仓库接入始终通过进程合同。
25. 作为 CLI 用户，我希望旧入口明确失败，以便不会误用另一套生命周期或产生空成功。

## Implementation Decisions

- 会话、record source、current recording、execution 和 material 遵循工具设计的
  生命周期、数据边界及数据所有权合同。
- agent record/mock 拥有协议 execution；official record/verify 拥有官方编排事实，
  两层通过实际 execution、connection 和 material 身份关联。
- official 命令选择官方 driver，driver 使用公开 agent 命令。agent record 不启动
  driver，agent mock 不绕过正常 execution。官方命令不预分配协议 execution。
- 录制材料在 agent record 完成时封闭；driver 的最终报告单独保存和关联，不能回写
  不可变材料。官方业务资格按设计中的身份、验证与显式采用合同核验。
- 远程桥接合并到 agent record 的传输实现，保留已有认证、EOF、退出与清理合同，
  不提供独立 connect 或 internal-protocol 命令。
- 协议包迁入 internal，复用已有采集、回放、原始 JSON 和录制格式能力。
- 新命令结构替代旧顶层 record/mock/verify 及 internal-protocol，不保留兼容别名；
  不兼容数据必须拒绝，不能静默解释或改写原始来源字节。
- official 活动登记、agent execution 和 finish 按工具设计协调，不持有妨碍子进程
  创建 execution 的锁；中断和失败保留事实，不生成虚假的完整结果。
- 同步 CLI 说明、术语、开发规范和 driver 请求合同；目标规则仅在工具设计维护。

## Testing Decisions

以 installed CLI 为统一纵向验收接缝。通过 argv、stdio、退出码、
descriptor、文件与哈希检查外部可观察行为，不以内部函数调用替代验收。
沿用现有 conformance suite、受控进程、synthetic 材料和 driver substitute。

覆盖[设计要求的测试边界](design.md#测试边界)，并特别验证原路径删除后的独立性、
record 成功/失败的 current 切换、验证不继承、consumer mock、导出再导入、
忙碌 finish 无修改，以及 closed 后导出和清理重试。
新增两条官方链路、远程采集、旧命令拒绝、编排与 execution 关联和取消清理覆盖；
不以源码字符串检查或内部函数测试代替进程链路验收。
逐 ticket 只做必要的构建和静态检查，不运行测试。全部 ticket 实现后，
集中执行开发规范要求的完整离线门禁，并修复失败；既定验收覆盖保持不变。

## Out of Scope

- 不启动真实 Agent、不新增真实录制、不安装官方 SDK，不执行网络验证。
- 不修改消费者 SDK、MASS 或产品 E2E 的业务编排。
- 不放宽严格回放，不改变 case 身份、required 集合或材料入仓政策。
- 不修改消费者仓库、不推送代码，不改写用户已有修改。

## Further Notes

Issue tracker 为仓库配置的 GitHub Issues，使用 ready-for-agent 标签。
任务按依赖顺序实施，每张 ticket 使用独立 chat 和分支，完成后合并回 main；
主 chat 负责编排、核对合并，并在全部 ticket 实现后统一测试与修复。
ticket 的实现、提交和合并遵循当前用户授权范围。
