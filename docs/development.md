# 开发规范

## Module 与依赖

使用单一 module `github.com/zoumo/agent-sdk-testify` 和单一工具二进制，
使用 Go 1.26 和 golangci-lint 2.12.2；不添加嵌套 module、replace 或 go.work。
生产代码及默认测试不得导入 agent-sdk-go、Go driver 或 SDK fixture 容器。
官方 SDK 仅作为 record/verify 的显式环境依赖，不在运行时自动安装。

消费者通过 mock 进程、场景/运行描述与独立回执接入，不通过跨仓库 Go 库。
公开业务入口限定为 record、verify、mock；材料资格、归档、采用和协议桥接为内部能力。

## 代码布局

```text
agent-sdk-testify/
  go.mod / go.sum / Makefile / .golangci.yaml
  main.go                   # 唯一根目录 Go 文件
  internal/
    record/ verify/ mock/   # 公开业务命令
    catalog/ corpus/        # case/catalog 与材料资格、采用、索引
    builtin/ official/      # 内置快照与官方 SDK 执行
    history/ tool/          # 历史派生与共享基础能力
  protocol/                 # 录制、严格回放、传输与命令接口
  data/
    scenarios/              # 权威 case、binding、输入与预期
    claude/ codex/          # 审核采用的 provider/case/attempt 材料
    index.json              # 固定索引
    builtin/                # 生成的发布快照
  drivers/                  # 官方 driver 与 Python wheel
  docs/ / scripts/ / README.md / LICENSE
```

布局变更时同步 imports、embeds、脚本、测试及文档。不要添加无需求的 cmd/pkg、
框架、转发层、动态插件或通用流程 DSL。

协议包不依赖 case 编号、driver 或 SDK。capture 拥有自己的 I/O、选项和类型化错误，
只在命令边界映射退出码。provider 的配置、启动参数和历史解释留在 provider 文件。
测试使用与消费者相同的进程接口，不导入被测 SDK 来间接证明工具正确性。

## 材料与测试输入

原件及失败尝试保存在 Git 外私有归档。已跟踪材料按 provider/story/case/attempt 归属；
固定索引保留所有要求、失败和缺口。移动不改变原始 plan/raw、attempt 身份或来源哈希。
派生运行副本须保留原/派生路径及双向哈希关联，不改写原件或放宽协议字段。
材料发布须遵守[工具设计](design.md#录制材料与发布)。

合成输入只用于专项测试，不能登记为真实录制或用户故事成功证据。

完成 verify、资格审核和采用后，用材料所在的完整 40 位源码 revision 重新生成内置
快照；普通版本变更也需要重新生成：

```sh
go run -tags builtin_generate . --revision FULL_40_CHARACTER_SOURCE_COMMIT
go run -tags builtin_generate . --check
```

生成器从固定索引选择 `admitted` 且 `verification=passed` 的材料，校验完整闭包并
复制原始字节。缺文件、哈希漂移、空选择或生成物过期必须失败；`scripts/build.sh`
和默认测试都会执行 freshness check。

## 编码约定

使用清楚的名称和语言惯用写法；注释解释原因，避免重复代码。
复用解决实际重复，性能优化需要实际需求。第三方依赖须有明确用途、版本与来源。
限制 lint 例外并说明原因；lint 不替代编译、测试和 race。
协议 stdout 与诊断分离，不记录认证秘密；可复用模块不修改全局日志或遥测配置。

## 测试

工具测试通过消费者实际使用的进程接口检查协议、环境、材料、回执和清理。
[共同验收](cases.md)定义 8 个用户故事、36 个 case；provider binding 的
required/optional、planned/unsupported 及原因必须保留。

| 层级 | 工具负责 | 归消费者负责 |
|---|---|---|
| 单元测试 | 协议匹配、材料身份与资格、配置和历史派生 | SDK 自身行为 |
| 受控进程集成 | CLI、stdio、严格握手、完整消费、EOF、退出、回执和清理 | SDK 公共接口、业务断言及 simulate |
| 官方离线验证 | 官方 driver 执行已有材料并检查业务与协议结果 | 当前 Go SDK 的仿真与覆盖门禁 |
| 真实 E2E / record | 仅显式授权执行，保留业务、采集和清理结果 | 产品自身 E2E |

默认测试离线，不启动真实 Agent、不需要认证、不安装官方 SDK、不新增录制。
进程测试保留分片、大消息、关联 ID、超时、取消、背压、多连接、历史派生、
workspace 隔离、prepare/run/finish 失败保留和自有资源清理。
合同和材料存在不证明当前 SDK 通过，合成材料必须保持 synthetic 身份。
官方验证、SDK 仿真和真实 E2E 分别报告，不互相背书。

### 离线门禁

使用 Go 1.26。完整门禁由 `make check` 执行，覆盖格式、lint、独立构建、默认测试、race、
vet、依赖边界、源码目录外运行、driver 测试和 wheel 打包检查。
case/索引身份、全部录制字节、来源哈希和政策检查不得弱化。

材料缺失、失败和未验证须保持可见；内置成功子集不能抵扣完整 required 覆盖。
新样本须经官方验证及既有审核；Go SDK 失败不使可信录制失去资格，也不能触发删帧或
放宽匹配。正式消费者使用可获取的固定发布版本；本地显式二进制覆盖仅用于联调并
记录实际身份。

### 测试编写

- 使用 `testify/assert` 与 `require`；后续步骤依赖检查时用 `require`。共享生命周期
  用 `testify/suite`，单函数检查用命名表驱动 case，不叠加第二套生命周期或断言框架。
- suite 内不调用 `t.Parallel`；并发、synctest 和时序敏感覆盖保留原生 `testing`。
  worker goroutine 不得调用 `require`/`FailNow`，结果交回拥有检查的测试 goroutine。
- 保留全部 case、预期值、错误分类、未知字段、背压、取消和竞态断言；不得降低覆盖、
  放宽 deadline，或用 mock 替换录制协议回放来让变更通过。
- Go `Example`、`TestMain`/子进程、测试入口、helper、清理和显式 live skip 在需要
  原生 API 时保持原生。修改生成器后重新生成并验证，不只手改输出。
- 真实 CLI/Agent 检查放在各原生 `tests/e2e` 下，保留 opt-in 门禁和默认 skip。
  capability、源码检查、离线/合成测试和真实录制证据分别保留，skip 不是 live pass。

### 环境与结果

测试使用自有临时资源，保留个人及其他任务的数据；失败时也释放己方资源并核对结果。
认证来自外部配置，日志和证据移除秘密。前置条件缺失或场景为空时报告未执行。
结果绑定实际源码、产物、环境和执行范围，保留失败证据；执行期间源码变化须说明。
通过、失败、未验证分别报告，筛选或局部通过不能代表全部通过，命令启动不代表完成。
