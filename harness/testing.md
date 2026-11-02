---
type: Development Standard
title: agent-sdk-testify 测试规则
status: stable
---

# Testing standards

工具测试通过已批准的进程接口检查协议、环境、材料、回执和清理。
[case 合同](sdk/validation-cases.md)定义 8 个用户故事、36 个 case；
provider binding 的 required/optional、planned/unsupported 及原因必须保留。
合同和材料存在不证明当前 SDK 通过，合成材料必须保持 synthetic 身份。

## 测试层级与自动化边界

| 层级 | 工具负责 | 归消费者负责 |
|---|---|---|
| 单元测试 | 协议匹配、材料身份与资格、配置和历史派生 | SDK 自身行为 |
| 受控进程集成 | CLI、stdio、关联 ID、严格握手、完整消费、EOF、退出、回执和清理 | SDK 公共接口、业务断言及 SDK simulate |
| 官方离线验证 | 官方 driver 执行已有材料并检查业务与协议结果 | 当前 Go SDK 的仿真与覆盖门禁 |
| 真实 E2E / record | 仅显式授权执行，保留业务、采集和清理结果 | 产品自身 E2E |

默认测试离线，不启动真实 Agent、不需要认证、不安装官方 SDK、不新增录制。
工具生产与默认测试依赖不得导入 agent-sdk-go；不以 replace、go.work、相邻 checkout
或自动跳过 SDK 断言解决迁移。SDK 专属断言由 SDK 承接；待迁移原件不参与 Go 测试发现，
须保留字节、哈希、原位置、明确归属和恢复任务。

工具从源码目录外运行编译产物，协议 stdout 只承载原生协议，诊断使用 stderr 或报告。
保留未知字段、数字字面量、关联 ID、严格匹配、错误分类、部分消费、取消、
受控子进程、多连接独立回执和清理断言。业务 passed 不掩盖缺失或失败回执。
工具不执行录制中的命令、文件编辑或模型调用；协议自回放不能代替 SDK 仿真。
官方验证、SDK 仿真和真实 E2E 分别报告，不互相背书。

## 离线工程门禁

使用 mise.toml 固定的 Go 编译器，执行独立构建、默认 go test ./...、race、vet、
生产及测试依赖边界检查、源码目录外运行和官方 wheel 打包检查。
case/索引身份、全部录制字节、来源哈希和政策检查不得弱化。
材料缺失、失败和未验证须保持可见；内置成功子集不能抵扣完整 required 覆盖。
新样本须经官方验证及既有审核；Go SDK 失败不使可信录制失去资格，也不能触发删帧或放宽匹配。
正式消费者使用可获取的固定发布版本；本地显式二进制覆盖仅用于联调并记录实际身份。

## 测试编写规则

- Use `github.com/stretchr/testify/assert` and `require` for assertions. Use
  `require` when later steps depend on the check; use `assert` for independent
  checks that can continue and report additional failures.
- Prefer `testify/suite` for shared setup/teardown, client lifecycle and related
  stateful scenarios. Use named table-driven cases for single-function checks;
  suite methods may contain table-driven subtests. Avoid wrapping a second
  lifecycle/assertion framework inside suite.
- Suite tests run sequentially: do not call `t.Parallel` inside them. Preserve
  concurrent, synctest and timing-sensitive coverage with native `testing`
  tables/subtests where needed. Keep internal worker concurrency and barriers.
- Never call `require`/`FailNow` from a worker goroutine. Collect worker and
  callback outcomes through channels, synchronization or a bounded collector,
  then assert on the owning test goroutine. Preserve cleanup and cancellation.
- Preserve all cases, expected values, error classifications, unknown-field
  checks, backpressure, cancellation and race assertions. Do not lower coverage,
  loosen deadlines or substitute mocks for recorded protocol replay to make a
  migration pass.
- Keep Go `Example` output contracts, `TestMain`/subprocess dispatch, testing
  entry points, helpers, cleanup, environment management and explicit live skips
  native where those APIs are required. Helpers should serve a concrete protocol
  or synchronization need; suite does not implement a fake agent or replay peer.
- Change the generator for generated tests, then regenerate and verify. Preserve
  every schema case and its native type/data; do not hand-edit only the output.
- Keep explicitly enabled real CLI/agent checks under each native `tests/e2e`.
  Retain opt-in gates and default skips. Document initialization/transport-only
  probes accurately; the directory name does not prove full model/task coverage.
- Keep capability, source inspection, offline/synthetic tests and real recording
  evidence separate. A skipped live check is not a live pass. Default tests and
  CI do not run real agents or collect recordings.

## 环境与结果

测试使用自有临时资源，保留个人及其他任务的数据；失败时也释放己方资源并核对结果。
认证来自外部配置，日志和证据移除秘密。前置条件缺失或场景为空时报告未执行。
结果绑定实际源码、产物、环境和执行范围，保留失败证据；执行期间源码变化须说明。
通过、失败、未验证分别报告，筛选或局部通过不能代表全部通过，命令启动不代表完成。
