---
type: Specification
title: 独立历史查询
status: stable
---

# 独立历史查询

历史查询可以不创建 Session。区分 metadata（身份、标题、预览、时间和可用状态）、保存的内容（原生 messages/turns/items）和原始事件日志。前两者不构成完整 wire 通知、delta、审批或 hook 回放；保留 raw source 也不建立全事件日志 API。

查询不能 Open、start、resume、subscribe、submit 或回答审批，不运行模型、不将保存的内容投影为历史完成事件、不注入 Receive、不建立成员关系或回答权限。已有同身份 Session 时，查询不套用 idle/stable 门禁；其真实 live 通知仍正常交付。查询 metadata 或 turns 可能包含已有 live 状态。结果保持原生顺序、重复、unknown 数据及调用方所有权，不补造缺失事实。

两个原生包独立实现，没有根统一 Client、统一历史 wire 协议、通用 Turn 或第二套 Session 流。共同验收见 [SDK-CASE-033、SDK-CASE-036 查询流程](../sdk/validation-cases.md#流程语义)；接口及具体后端的支持状态由外部实施证据记录，不从本合同推定已完成共同能力。

## Codex 原生接口

`codex.Client` 提供以下独立方法，参数和结果是 `codex/types` 的原生绑定：

```go
ListThreads(context.Context, types.ThreadListParams) (types.ThreadListResponse, error)
ReadThread(context.Context, types.ThreadReadParams) (types.ThreadReadResponse, error)
ListThreadTurns(context.Context, types.ThreadTurnsListParams) (types.ThreadTurnsListResponse, error)
ListThreadItems(context.Context, types.ThreadItemsListParams) (types.ThreadItemsListResponse, error)
```

每次调用只返回一页或一次 read，不自动取全。`Optional` 区分 omitted/null/value；保留字段、unknown 变体及新服务端 readState/readStates 扩展，不用旧绑定猜测新字段含义。推荐 `ReadThread` 显式 `IncludeTurns: types.Some(false)` 读取 metadata。summary、full items、完整原始日志是三种不同覆盖，full 仍仅表示保存的 ThreadItem。

turns 原生默认 desc/summary，items 默认 asc。页大小、过滤和排序由固定服务端版本决定，不建立 SDK 的统一分页默认。cursor opaque；保留 nextCursor 和 backwardsCursor，显式 null 表示当前无后页，省略不能改写为 null。反向 turns anchor 可再次包含同一 turn；item anchor 排他，必须提供非空 turnId，后续继续使用原生 opaque cursor。SDK 不静默排序、去重、clamp 或补字段；无 snapshot token，不承诺跨页或与 live 状态一致的原子快照。

list 默认 interactive、non-archived 范围，不能称全部历史。保留 source/provider/archive、cwd、project/section、parent/ancestor、originator、sort 和 useStateDbOnly 等原生选项；本地与 hosted 的支持条件可以不同，原生错误不能改为空列表。版本、legacy history、DB/metadata 条件可能使 items/list 不支持，turns legacy fallback 可能逐页重建完整 rollout；SDK 不自动 resume 或改成事件回放。

查询经初始化后的 app-server 连接。构造 stdio 连接可启动进程，原生 list 可扫描及修复 metadata；不承诺零进程或零磁盘写，useStateDbOnly 保持原生含义。远端查询使用服务器存储及路径语义，不读取客户端 transcript，也不假定远端一定使用 LocalThreadStore。保留原生拒绝、协议/传输错误及 context 取消和已有 dispatch 证据，不自动重试。

## Claude 查询范围

下面三个章节分别定义[主消息](#messages-claude-独立本地消息查询)、
[metadata](#metadata-claude-独立本地发现与-metadata)和[子历史](#children-claude-独立本地子历史)。
它们都是显式本地 scope 下的无进程查询，不改变 Session 的 live 路由或权限。
metadata 的采样预览与主消息链分开；子链使用独立算法，不能复用主链恢复。
独立子历史也不替代 Session.ListSubagents。独立父子消费流与远端子订阅不在本合同内。

<a id="messages-claude-独立本地消息查询"></a>

## Claude 独立本地消息查询

原生包提供 `GetSessionMessages(ctx context.Context, sessionID string,
options SessionMessagesOptions) (SessionMessagesPage, error)`。
`SessionMessagesOptions` 包含 `Scope SessionQueryScope`、Limit/Offset、
MaxMessages/MaxBytes/MaxLines/MaxRecords/MaxLineBytes。Scope 包含显式绝对
ConfigDir、可选绝对 ProjectDir 和 MaxEntries。Page 包含原生独立
`[]SessionMessage` 与 `SessionMessagesCoverage`；错误为可判别的 SessionQueryError
或 context 取消。消息值不使用 Event、Received 或 IncomingRequest。

<a id="messages-scope-与值"></a>

### Scope 与值

`SessionQueryScope{ConfigDir, ProjectDir, MaxEntries}` 是可复用的无权限查询范围。
ConfigDir 必须显式绝对路径，不读取用户 HOME、环境默认目录或客户端历史。
ProjectDir 可选且必须绝对，解析现有项目目录的 symlink 后使用原生非字母数字替换
为 `-` 的项目 key。超过 200 字符的截断 key 不支持。未指定项目时只发现
ConfigDir/projects 的直接项目子目录，扫描所有项目后才确认唯一匹配；同一 ID
多处匹配报 ambiguous。只覆盖该布局下主会话 `<UUID>.jsonl`，不展开 worktree、
不运行 git，不搜索其他配置或旧布局。programmatic 会话仅在同一布局下可读。
不包含子代理消息、system inclusion、SessionStore 或云后端。

`SessionMessage` 携带 Type、UUID、SessionID、可用 ParentUUID/Timestamp、原生
Message JSON 和整个原始 Raw。Raw 保留未知 envelope 字段、omitted/null/value
和大整数；Message 保留未知内容块与数字，不经过 float64。
值与源和后续调用 detached，无 IncomingRequest 或任何回答方法。Raw 是源检查
材料，不构成完整 wire 通知、delta、审批或 hook 回放 API。

<a id="messages-选择与切片"></a>

### 选择与切片

先对完整有界源建索引，找 terminal 的最近 user/assistant，优先选择非
sidechain/team/meta 的最后文件位置主叶；无主叶时沿最后候选叶回溯并过滤。
仅沿 parentUuid 重建根到叶链，不沿 compact_boundary.logicalParentUuid
重复压缩前内容；保留 isCompactSummary。相关并行 tool_use/tool_result 按原生
response message.id、直接 parent、sourceToolAssistantUUID 和同 writer 的工具
调用身份恢复；工具调用跨 response 歧义时不用于匹配 stray，已回答调用不恢复
额外 stray。恢复内容遵循原生稳定 timestamp 排序及 tie 次序，父链不另排序。
最终仅返回非 sidechain/team/meta 的 user/assistant，再应用 Offset/Limit。

Limit/Offset 非负，Limit=0 使用 SDK 有界默认 1000；MaxMessages 默认 10000，
请求 Limit 超过此上限报 resource-limit。超页尾和有效空源成功返回空 Messages。
无 native cursor、nextCursor 或 total；切片不是 I/O 分页，每次可能扫描整个有界源。

<a id="messages-资源错误和一致性"></a>

### 资源、错误和一致性

零值默认 MaxBytes=64 MiB、MaxLines=100000、MaxRecords=100000、
MaxLineBytes=8 MiB、MaxEntries=10000。MaxLines 包括空行和 metadata，MaxRecords
包括全部索引链条（含被过滤分支）。所有上限和切片参数可配置，负数拒绝。
每条 JSONL 必须换行结束。配置根被显式选定并固定，树内目录和文件必须是普通
目录/regular file，不接受 symlink；os.Root 约束树内路径替换不能逃逸配置根。
文件以非阻塞/no-follow（平台支持时）打开，避免 FIFO 阻塞；不读取私有外部路径。

`SessionQueryError{Kind, Path, Reason, Raw, Cause}` 可判别 invalid-input、missing、
inaccessible、corrupt、unsupported、ambiguous、resource-limit、changed；Cause
保留 OS 错误。取消保留 context 错误。覆盖 Status 为 unknown、partial 或 complete，
返回实际范围、字节、行、记录、发现数量与有效 Limits。任何错误都不返回部分消息，
不把 partial 截断为成功。duplicate key/UUID、cycle、missing-parent、无效 UTF/JSON、
冲突身份等报 corrupt；未来未知 entry type 报 unsupported。已确认的非消息记录
api-request-shape、api-request-blob、api-request、cost-state 在独立查询中不进入
消息链、无 UUID/parentUuid 要求；仍先验证完整 JSON 和可用 sessionId/session_id
身份，并计入源字节、行及单行上限。此分类不扩大旧 replay 的 frozen-tail 许可。

读取结束前重读并比较整个源哈希、长度、mtime 和原根相对路径文件身份；观察到
append、rewrite、文件/目录 replacement 报 changed。Atomic 始终 false：文件系统
没有事务锁，校验之后的写入、两次校验间完全恢复的更改、配置根句柄外部改名不保证
检测。各页/各调用没有共同快照，调用者应自行决定重试策略。文件存在不证明 running。

<a id="messages-专项验收"></a>

### 专项验收

使用隔离合成存储及公开接口验证下列断言；不计为真实流程录制证据。

- 用户目标：不运行或恢复 Claude，独立读取显式本地范围的已保存主会话消息。
- 公开路径：原生 package-level GetSessionMessages；使用隔离 ConfigDir、项目和合成 JSONL。
- 内容验收：分支主叶、parallel 工具调用及结果兄弟、stray writer/已回答限制、压缩摘要及 logical parent 不回溯、meta/sidechain/team 过滤、大整数及 unknown Raw 均保留原生语义。
- 切片验收：可见消息选定后应用非负 limit/offset，默认返回有界；有效空、页尾空与失败可区分。
- 失败验收：损坏、重复、cycle、missing-parent、未知布局/类型、发现/字节/行/索引/返回量上限、取消、missing、inaccessible、歧义、路径隔离均可识别；失败不返回部分消息。
- 隔离验收：runtime 不可执行仍可查询，只读 fixture 哈希和权限不变；值 detached，不产生 live Event、回答权限或成员关系。已有 Session 的正常通知继续独立交付。
- 完整性验收：对读取期间 append/rewrite/replacement 作必要的内部故障注入，报告检测覆盖边界，不承诺跨页原子快照。

<a id="metadata-claude-独立本地发现与-metadata"></a>

## Claude 独立本地发现与 metadata

原生包提供 `ListSessions(ctx context.Context, options ListSessionsOptions)
(SessionsPage, error)` 与 `GetSessionInfo(ctx context.Context, sessionID string,
options SessionInfoOptions) (SessionInfoResult, error)`。复用 SessionQueryScope，
metadata 与消息分离。ListSessionsOptions 包含 Scope、Limit/Offset、MaxPage/
MaxSessions、SampleBytes/MaxBytes/MaxLines；SessionInfoOptions 只含 Scope 与
SampleBytes/MaxBytes/MaxLines。SessionsPage 包含 []SessionInfo 与
SessionMetadataCoverage；SessionInfoResult 包含可空 *SessionInfo 与同型 Coverage。
错误可伴部分值，不授予回答权限。

两者不需要 Client、Session 或 runtime，复用[消息查询](#messages-claude-独立本地消息查询)
的 `SessionQueryScope`、主源定位、普通目录/文件约束及 `SessionQueryError`。
不执行 Open、resume、control、订阅、模型、写入或 git，不改变 live 交付及回答权限。
应用按 list → info → GetSessionMessages 选择保存内容；metadata 与消息分别查询。

<a id="metadata-范围与值"></a>

### 范围与值

必须显式指定绝对 ConfigDir。可选绝对 ProjectDir 使用消息查询的原生项目 key；
未指定时仅扫描 projects 的直接项目目录及其直接主源 `<UUID>.jsonl`。树内 symlink
拒绝。最多两层发现，不递归展开 worktree、子代理、旧布局或 programmatic 专用
存储；同一主源布局下的 programmatic/daemon 会话包括在内，不按入口过滤。
已知 `<UUID>/` 子代理容器与 regular `sessions-index.json` 不作为主源读取，未知
项目条目报告 unsupported，不以未知布局假装完整空列表。显式项目不存在报 missing。
本地后端不读取远端或其他用户配置；不提供 alpha SessionStore 或云后端。

`SessionInfo` 保留 SessionID、Path、FileSize、文件 LastModified（Go time.Time）、
可用 CustomTitle/AITitle、FirstPrompt、GitBranch、Cwd、Tag、Timestamp/CreatedAt。
Summary 是有界预览，优先最后非空 customTitle、aiTitle、lastPrompt、summary，
最后回退首条有效 user 提示词。FirstPrompt 只来自 head；排除 tool_result、
meta/compact summary/sidechain/team 与原生自动生成前缀，command 仅作后备。
仅首个有完整来源证据的记录将整个文件标为 sidechain/team 时排除该源；后续隐藏
分支不排除主源。有效空和没有已知字段的 metadata 仍返回身份/stat；无可用摘要
时 Summary 为空，不补造标题、cwd、时间或主链事实。Running 始终 unknown。

Head/Tail 是调用者所有的原始采样 bytes，可能含边界半行；Records 是完整采样
JSON object 原文，保留 unknown、null/省略、大整数和未知类型。所有返回值 detached，
不构造 Event 或 IncomingRequest。采样原文不等于完整消息或原始 wire 日志。

<a id="metadata-资源排序及切片"></a>

### 资源、排序及切片

ListSessionsOptions 含 Scope、Limit/Offset、MaxPage/MaxSessions、SampleBytes/
MaxBytes/MaxLines；SessionInfoOptions 只含 Scope 与采样字节/行上限。所有参数
非负，零值默认：Limit=100、MaxPage=1000、MaxSessions=10000、MaxEntries=10000、
SampleBytes=64 KiB（各 head/tail）、MaxBytes=16 MiB（每次调用累计初读字节）、
MaxLines=10000（完整采样行，包括空行）。发现范围/中间排序库存先受上限约束，
再按 LastModified 降序排序并应用 Offset/Limit；同时间以 Path 升序为 SDK tie
规则，与官方平台目录迭代 tie 不保证相同。保留重复身份并报 ambiguous，不去重。
无 native cursor/nextCursor/total。超页尾及完整空发现成功返回空 slice。

info 使用同一定位器，只采样所选文件；不会因单源 metadata 而解析全历史。
小于两倍 SampleBytes 的源只读一次连续内容；更大源跳过中段并设 Sampled=true。
分离尾窗额外读取其前 1 字节来判断首行是否完整；该探测计入 MaxBytes/Bytes，
随采样一起重读验证，不加入原始 Tail，也不提供 head 来源事实。首尾不完整
边界行不解析，Head/Tail 仍保留其 bytes；源末行缺少换行报 corrupt。
默认的采样窗口不构成完整链、内容可用性或整个文件有效性的认证。

<a id="metadata-错误覆盖与一致性"></a>

### 错误、覆盖与一致性

Coverage 暴露 Status=unknown/partial/complete、实际 ConfigDir/ProjectDir、Entries、
Files、Lines、Bytes、有效 Limits 和 Atomic=false。complete 仅表示本次声明发现
及采样完成，Sampled=true 时依然有未读中段。列举可以同时返回部分身份/元数据和
error；单源采样失败也保留已得到的 Info。调用者必须同时检查 error/Coverage。
完整空、被明确排除（Info=nil 且无错）、missing、访问失败、corrupt、unsupported、
ambiguous、resource-limit、changed 与 context 取消分别可辨认。多故障以 errors.Join
保留原始 SessionQueryError/OS cause，不把 partial 截断为成功。

文件必须 regular，读取使用受限 root 和平台 no-follow/nonblocking 打开。采样后
重读同一首尾，比较内容、大小、mtime 及原根相对路径身份；观察到 append、采样
rewrite、文件/目录 replacement 报 changed。校验 I/O 至多初读字节的两倍；Bytes
只计初读，不计验证。未读中段同大小且恢复 mtime 的 rewrite、两次检查间完全恢复
的变更、最后检查后的写入、显式配置根自身外部改名可能无法检测。发现集合本身
不加锁，不承诺跨文件或跨页原子快照；调用者负责重试和范围选择。

<a id="metadata-专项验收"></a>

### 专项验收

使用隔离合成存储及公开接口验证下列断言；不计为真实流程录制证据。

- 共同目标：无需 Session，从声明范围发现身份、检查 metadata，再选择读取保存内容。
- 原生公开路径：ListSessions → GetSessionInfo → GetSessionMessages，隔离临时 ConfigDir/project、合成只读 JSONL，无 runtime 和真实模型。
- 内容：可用时间、title/preview、unknown/null/大整数、无已知 metadata 的空/未知记录保留；Running unknown；值 detached，文件哈希和权限不变。
- 范围：显式项目过滤与 ConfigDir 全项目、无 worktree expansion、同布局 programmatic、已知子代理容器排除、symlink/未知布局及路径隔离可识别。
- 切片：降序 lastModified，tie 的明确 SDK 规则，非负 offset/limit 与有界默认页，超页尾成功空；重复身份保留并报歧义。
- 覆盖：完整空、missing、inaccessible、corrupt、partial、unknown、资源超限、取消可辨识；部分身份可伴 error，不静默成功。
- 采样：首尾预览不认证中段或完整链，单源 info 不强制全历史读取，首尾边界及截断尾行覆盖；源变化检测另用必要内部故障注入补充。
- 并存：历史查询不增加 live 消费者、协议写入、成员或回答权限；原 live 通知继续交付，原审批句柄仍按原权限使用。

<a id="children-claude-独立本地子历史"></a>

## Claude 独立本地子历史

子历史使用下述独立 API、布局与链选择规则；只读与 live 隔离遵循本页共同查询约束。

<a id="children-固定公开-api-与值"></a>

### 固定公开 API 与值

以下声明位于 claude 运行时包；既有 Session.ListSubagents/SubagentQuery 不改名，
不使用它的冻结进程环境冒充显式独立查询 scope：

```go
func ListSubagents(context.Context, string, ListSubagentsOptions) (SubagentsPage, error)
func GetSubagentMessages(context.Context, string, string, SubagentMessagesOptions) (SubagentMessagesPage, error)
// string 参数依次为 parentSessionID、agentID；List 只有 parentSessionID。
type SubagentHistoryLimits struct {
    MaxBytes int64
    MaxLines, MaxRecords, MaxLineBytes int
    MaxFiles, MaxDirectories, MaxDepth int
    MaxSidecarBytes int64
}
type ListSubagentsOptions struct {
    Scope SessionQueryScope
    Limits SubagentHistoryLimits
    Limit, Offset, MaxAgents int
}
type SubagentMessagesOptions struct {
    Scope SessionQueryScope
    Limits SubagentHistoryLimits
    Limit, Offset, MaxMessages int
}
type SubagentInfo struct {
    SessionID, AgentID, Path string
    ParentAgentID, ParentToolUseID *string
    Sidecar json.RawMessage
    Running string // 始终 "unknown"
}
type SubagentMessage struct {
    SessionMessage // 原 transcript 的 Type/UUID/SessionID/ParentUUID/Timestamp/Message/Raw
    AgentID string // 所选文件身份，不从 message text 推断
    ParentAgentID, ParentToolUseID *string // 仅来自 sidecar
}
type SubagentHistoryCoverage struct {
    Status string // unknown、partial、complete
    ConfigDir, ProjectDir, SessionID, AgentID, MainPath, Root, Path string
    Directories []string
    Entries, Files, Lines, Records int
    Bytes int64
    Atomic bool // 始终 false
    Limits SubagentHistoryLimits
    MaxEntries, Limit, Offset, MaxResults int // 生效值；MaxResults 对应 MaxAgents/MaxMessages
    SidecarStatus string // list 为 "per-record"；get 为 absent/present
}
type SubagentsPage struct {
    Records []SubagentInfo
    Coverage SubagentHistoryCoverage
}
type SubagentMessagesPage struct {
    Info SubagentInfo
    Messages []SubagentMessage
    Coverage SubagentHistoryCoverage
}
```

指针 nil 表示未提供或显式 null，差异保留于原始 Sidecar/Raw；不把缺 parentAgentId
推成根的直接 child。所有 JSON/bytes/slices/pointers detached，修改结果不影响下次
查询或 Session。JSON 保留未知字段/块、大小写、大整数及 null/省略，不经过 float64。
Sidecar 原文和查询错误 Raw 可能私密，错误字符串不自动打印它们。

<a id="children-定位布局及身份"></a>

### 定位、布局及身份

复用 SessionQueryScope：ConfigDir 必须显式绝对；可选 ProjectDir 必须绝对，使用
现有 realpath 项目 key（超过 200 字符 key 不支持），不扩展 worktree/cloud/alpha
SessionStore/旧布局。不提供任意 transcriptPath 输入。父 ID 必须匹配既有 native
session UUID 校验；agentID 必须有效 UTF-8、1–255 bytes，拒绝 .、..、/、反斜线、
NUL、控制字符及驱动器分隔冒号。agentID 不是 UUID，保持大小写和 Unicode 原文，不做 NFC 或大小写归一；不同 ID 按原始 bytes 比较，实际文件别名按下述冲突规则处理。

先在声明项目范围内定位唯一 `<parentUUID>.jsonl`，跨项目冲突报 ambiguous。
要求该主源存在（空源允许）；有界完整语法/身份校验但不重建主链，源内可用
sessionId/session_id 必须等于所选父 UUID。随后仅扫描其旁
`<parentUUID>/subagents/`，包括任意普通嵌套子目录如 workflows/<runID>/。
目录位置不证明 parentAgentID，nested path 不等于 agent tree。

只识别 regular `agent-<agentID>.jsonl` 及同目录 `agent-<agentID>.meta.json`。
其余普通文件/特殊节点/不支持布局报 unsupported；不静默忽略。孤立 sidecar 报
corrupt。空 subagents/ 或父存在且容器缺失时 list 成功为空，get 为 missing。
同一 agentID 在任意两个相对路径、或两个候选由文件身份检查确认是同一个文件时报
ambiguous，绝不第一项获胜/去重。list 按相对 Path 的 UTF-8 bytes 升序，再切片；
get 即使已找到目标也完成有界库存扫描，证明唯一性，不只查顶层。

list 读取全部候选及 sidecar 并验证语法/身份（不重建每个 child 的链）；get 扫描
库存后只读主源、所选 child 和其 sidecar。内容中可用 sessionId/session_id 必须
匹配 parentSessionID，可用 agentId/agent_id 必须匹配文件 agentID；冲突为 corrupt。
不要求消息 UUID 是 native session UUID，仅要求非空字符串且唯一。

sidecar 是整个 JSON object；toolUseId、parentAgentId 允许省略/null/非空 string。
parentAgentId 为非 null string 时须满足 agentID 校验且不得等于自身；它不要求父文件存在。
sidecar 中 sessionId/session_id、agentId/agent_id 若出现，必须分别为匹配父 UUID/文件 agentID 的非空 string（null 为 corrupt）；其他未知字段原样保留。缺 sidecar 成功，两个 parent 值
unknown；损坏、权限失败、字段类型错误、来源身份冲突均返回错误，不降级成缺失。
SidecarStatus 和 Sidecar 原文区分 absent 与空 object。不会把 sidecar 历史关系
注册为 live 成员、创建事件或回答权。

<a id="children-子链选择与切片"></a>

### 子链选择与切片

单独的 child 算法：索引 user/assistant/progress/system/attachment（均需非空 uuid），
选择文件中最后一个 user/assistant 作为叶，沿 parentUuid 回到 null/省略，反转成
根到叶，仅返回该链 user/assistant，再 Offset/Limit。没有叶的有效源成功为空。
不按 timestamp 重排，不按 message.id 去重，不过滤 isSidechain/isMeta/teamName，
保留原文；不沿 compact_boundary.logicalParentUuid，不恢复主历史 parallel-tool
siblings/stray。重复 UUID、cycle、非空 parentUuid 找不到节点均为 corrupt（即使
在未选分支）；所有链记录先校验，再选择。未知 entry type 为 unsupported；既有
主查询已审阅的 metadata 及 api-request-shape/api-request-blob/api-request/cost-state
只做完整 JSON/可用身份校验，不进子链。未知 native message/content 字段仍保留。

Offset/Limit 非负；list 的零 Limit=100、MaxAgents=10000；get 的零 Limit=1000、
MaxMessages=10000。请求 Limit 超过对应 MaxResults 返回 resource-limit；库存
agent 数超过 MaxAgents 同样失败，get 索引与可见返回分别受 MaxRecords/MaxMessages
约束。Offset 超页尾及有效空源成功为空。无 cursor/nextCursor/total，不是 I/O 分页，
不会自动补齐/重试。saved content/Raw 不等于 wire delta/控制事件日志。

<a id="children-资源错误与一致性"></a>

### 资源、错误与一致性

所有限制负数 invalid-input；零默认 MaxBytes=64 MiB、MaxLines=100000、
MaxRecords=100000、MaxLineBytes=8 MiB、MaxFiles=10000、MaxDirectories=10000、
MaxDepth=64、MaxSidecarBytes=64 KiB、Scope.MaxEntries=10000。限制按整次调用累计，
先限制再分配/读取/排序；深度以 subagents 根为 0。Files 包括主源、被读取 child
和存在的 sidecar；Entries 包括项目定位及递归库存中的全部目录项；Lines 包括
空行/metadata/sidecar 的物理行（sidecar 可无末换行），Records 包括全部已索引主/子
链条而非仅返回消息；Bytes 包括全部初读（主源+child+sidecar）。目录数含项目定位
中打开的目录及 subagents 根/嵌套目录，MaxDepth 只限制 child 库存遍历。
JSONL 每行完整 UTF-8 object 且末尾换行；重复 JSON key、无效 JSON/UTF、缺链身份
为 corrupt。sidecar 同样完整 JSON/无重复 key，并同时受 MaxLineBytes/MaxSidecarBytes。

复用 SessionQueryError.Kind：invalid-input、missing、inaccessible、corrupt、
unsupported、ambiguous、resource-limit、changed；保留 Path/Reason/Raw/Cause，
errors.Is 可见 OS cause，取消保留 context 错误。两 API 任意错误均清空 Records/
Messages（Info 也清空），仅保留实际 Coverage，绝不部分成功。unknown 是未定位，
partial 是已定位但未完成，complete 仅证明本次声明布局/读集合完成；不证明 running
或所有平台布局支持。MaxResults 只限制结果，不隐藏扫描超限。

显式配置根可解析其自身 symlink 后固定句柄；树内目录及文件拒绝 symlink，使用
os.Root containment、regular-file、no-follow/nonblocking 打开，不读 FIFO/device/
外部路径。检测目录/文件替换不能逃出 root。支持平台能力不足报 unsupported。

返回前重新验证发现目录的完整条目集合/类型/身份以及每个读文件的身份、size、mtime
和完整内容哈希；不存在的可选 sidecar/容器也重查。观察到 append/rewrite/replacement/
新增/删除/sidecar 变化均为 changed。验证读取至多初读字节的两倍（包括初读），
Bytes 只计初读；无锁，Atomic=false。最后验证之后写入、两次检查间完全恢复的
变更、根句柄外部改名不保证检测；跨文件/页/调用没有共同快照。无内部自动重试。

<a id="children-专项验收"></a>

### 专项验收

使用隔离合成存储验证，错误与变化通过确定性屏障注入；不计为真实子历史证据。

| 场景 | 公开操作与独立预期 |
|---|---|
| 独立查询 | 临时显式 ConfigDir，经 package ListSubagents → 选 agent → GetSubagentMessages，CLIPath 不存在且所有 live flag=0；无 Session/进程/模型 fallback。已有同身份 Session 时以 peer barrier 证明无 resume/control/Receive 注入。 |
| 布局与身份 | direct、workflows/run 及多级目录；非 UUID agentID；缺 sidecar 保持 nil，存在则精确保留 toolUseId/parentAgentId/unknown/null。跨项目重复父 ID、跨路径重复 agentID、身份冲突分别失败，路径目录不能制造父边。 |
| 子消息链 | 合成分叉 fixture 的最后 user/assistant 为叶；根到叶再切片，isSidechain/isMeta/team 不过滤。故意放 compaction logicalParentUuid 与同 response 的 stray tool_result，断言两者不被主历史恢复算法接入。重复 UUID、cycle、missing parent 为 corrupt；message unknown、大整数和 detached 所有权保留。 |
| 上限与错误 | nil/cancel context、负数/非法 ID、显式项目缺失、空源/无叶/超页尾、无 child 容器、孤立/坏/无权限 sidecar、partial 最后一行、坏 JSON/UTF、未知类型、目录/文件/行/byte/index/depth/result 上限，逐项检查 Kind/Cause/Coverage，错误无部分值。 |
| 路径隔离与变化检测 | symlink/FIFO/device/文件及目录 replacement 不逃 root；测试 barrier 注入 append、rewrite、库存增删、sidecar 出现/消失返回 changed。不能靠 sleep 证明一致性；固定 Atomic=false 和跨调用限制。 |
