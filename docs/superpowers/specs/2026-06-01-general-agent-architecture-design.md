# 通用 Agent 架构设计

日期：2026-06-01

## 背景

当前项目已经转向 Go 实现的通用 agent 管理员 Dashboard。目标是在此基础上实现一个通用自主任务 agent。第一版面向单用户本地运行，但架构必须保留未来升级为多用户服务端系统的能力。

第一版 agent 支持文件系统和网页搜索两类工具。自主程度需要可配置，默认建议为中自主：低风险动作可自动执行，中高风险动作需要用户确认，高风险动作默认拒绝或不注册为工具。

前端默认不是普通聊天页，而是管理员 Dashboard。管理员可以看到所有 agent run 的状态、步骤、决策摘要、工具调用参数、工具执行结果、审批记录、产物和审计事件。

## 目标

- 提供一个通用 agent 内核，支持围绕目标进行 `plan -> act -> observe -> decide` 循环。
- 第一版单进程本地运行，沿用当前 Go Web 服务形态。
- 内部模块边界按未来多用户、异步任务、worker 拆分预留。
- 支持可配置自主程度，通过统一 Policy Engine 控制工具权限。
- 首批工具支持文件系统和网页搜索。
- 默认前端为管理员 Dashboard，展示完整可观测执行细节。
- 所有工具调用、审批和结果都可审计、可回放。

## 非目标

- 第一版不做多租户账号系统。
- 第一版不做分布式 worker 调度。
- 第一版不支持任意 shell 命令执行。
- 第一版不支持删除文件。
- 第一版网页搜索工具不做登录、表单提交、上传、下载、支付、发消息等浏览器自动化动作。
- 不依赖模型供应商返回完整隐藏链式推理；产品展示使用结构化决策摘要和工具观察结果。

## 推荐路线

采用模块化 Agent Core。

第一版仍是单进程 Go 服务，但内部按以下边界拆分：

- `Web UI / API`
- `Run Service`
- `Agent Core`
- `Policy Engine`
- `Tool Registry`
- `Tool Executor`
- `Memory Store`
- `Audit Log`
- `Filesystem Tool`
- `Web Search Tool`

后续升级时，可以把 `Run Queue`、`Tool Executor`、`Memory Store`、`Approval Service` 拆成独立服务或 worker，而不重写 agent 决策协议。

## 总体架构

```text
Admin Dashboard / API
        |
        v
Run Service
        |
        v
Agent Core  <---- Context Builder <---- Memory Store
        |
        v
Policy Engine
        |
        +---- allow ------------> Tool Executor ----> Tool Registry ----> Tools
        |
        +---- require_approval -> Approval Queue
        |
        +---- deny -------------> Observation

All state changes -> Audit Log
```

### Web UI / API

负责创建任务、展示 run、展示步骤时间线、处理审批、暂停/恢复/取消任务。

第一版前端默认是管理员 Dashboard，核心区域包括：

- Run 列表：展示所有 run 的目标、状态、更新时间、自主等级。
- 状态筛选：按 running、waiting_approval、blocked、failed、completed 过滤。
- 时间线：展示每个 step 的模型决策、决策摘要、工具调用、观察结果和错误。
- 工具详情：展示工具名、参数、风险等级、Policy 决策、审批状态、结果和耗时。
- 审批队列：支持 approve once、deny、allow for this run。
- 产物列表：展示生成文件、搜索来源、报告等引用。
- 审计日志：展示 actor、action、timestamp、payload。

### Run Service

负责一次 agent run 的生命周期编排：

- 创建 run。
- 加载上下文。
- 调用 Agent Core 获取下一步决策。
- 校验决策格式和工具参数。
- 调用 Policy Engine。
- 执行工具或进入等待审批。
- 保存 step、tool call、observation 和 audit event。
- 产生 runtime event；Web 层通过 SSE 让管理员 Dashboard 观察这些事件。

### Agent Core

Agent Core 只决定下一步要做什么，不直接访问文件系统或网络。它每轮接收 `RunContext`，输出一个结构化 `AgentDecision`。

决策类型：

```text
respond     回复用户，不调用工具
call_tool   请求调用一个工具
ask_user    缺少信息，暂停等待用户输入
complete    任务完成，给出最终结果和产物引用
```

示例：

```json
{
  "type": "call_tool",
  "reasoning_summary": "需要先读取项目结构，确认可修改范围。",
  "tool_name": "filesystem.list_dir",
  "arguments": {
    "path": "."
  }
}
```

`reasoning_summary` 是面向审计和管理员展示的决策摘要，不代表完整隐藏链式推理。

### Context Builder

每轮为 Agent Core 构造上下文：

- 用户目标。
- run 配置：自主等级、允许工具、路径范围、最大步数、超时。
- 当前状态：run status、step index、失败次数。
- 历史摘要：已完成动作、发现、失败尝试。
- 最近 observation：工具结果、错误、用户审批结果。
- 工具契约：工具名、schema、能力边界、风险说明。
- 系统策略：不能越权，不能绕过 Policy，不确定时使用 `ask_user`。

### Policy Engine

所有自主程度都由 Policy Engine 控制，不把权限判断散落在 Agent Core、Tool Executor 或前端。

Policy 输入：

- run 的自主等级。
- tool intent。
- 工具风险等级。
- 工具参数。
- 当前授权范围。
- 历史审批记录。

Policy 输出：

```text
allow             自动执行
require_approval 进入确认队列，run 状态变为 waiting_approval
deny              拒绝动作，拒绝原因作为 observation 返回给 Agent Core
```

自主等级建议：

- 低自主：绝大多数工具调用都需要确认。
- 中自主：读文件、网页搜索自动允许；写文件、覆盖文件、敏感路径、大量读取需要确认。
- 高自主：白名单范围内自动执行更多动作，但删除、越权路径、敏感外传仍然拒绝或确认。

默认配置为中自主。

### Tool Registry 和 Tool Executor

Tool Registry 登记工具契约：

- 工具名。
- 输入 schema。
- 输出 schema。
- 风险评估函数。
- 能力边界说明。
- 执行器。

Tool Executor 只执行已被 Policy 允许的工具调用，不自行判断权限，不直接修改 run 状态。

工具接口示意：

```go
type Tool interface {
    Name() string
    Schema() ToolSchema
    Risk(args json.RawMessage) RiskLevel
    Execute(ctx context.Context, args json.RawMessage) (ToolResult, error)
}
```

## 执行状态机

run 状态：

```text
queued
running
waiting_approval
needs_input
blocked
completed
failed
cancelled
```

step 循环：

```text
build context
-> ask model for next action
-> validate model decision
-> policy decision
-> execute tool or pause
-> observe result
-> persist step
-> decide next step
```

失败收敛策略：

- JSON 解析失败：要求模型重试一次。
- schema 不合法：把错误作为 observation 回给模型。
- 同类错误连续超过阈值：run 进入 `blocked`。
- 超过最大步数：run 进入 `needs_input` 或 `blocked`，请求用户确认是否继续。

## 数据模型

第一版建议使用 SQLite 或轻量本地持久化。即使单用户，也保留未来多用户边界。

核心记录：

```text
owners        未来多用户边界；单用户版固定 default owner
sessions      一组对话或任务集合
runs          一次 agent 任务执行
steps         agent loop 的每一步
tool_calls    工具调用意图、参数、结果、policy 决策
approvals     用户确认记录
artifacts     文件、报告、搜索结果快照等产物引用
audit_events  审计日志
```

关键字段：

```text
runs:
  id, owner_id, session_id, goal, status, autonomy_level,
  created_at, updated_at, started_at, completed_at

steps:
  id, run_id, index, type, status,
  model_input, model_output, reasoning_summary,
  observation, error

tool_calls:
  id, run_id, step_id, tool_name, arguments_json,
  risk_level, policy_decision, approval_status,
  status, result_json, error, started_at, finished_at

approvals:
  id, run_id, tool_call_id, status, reason,
  requested_at, decided_at, decided_by

artifacts:
  id, run_id, kind, uri, metadata_json, created_at

audit_events:
  id, owner_id, run_id, actor, action, payload_json, created_at
```

## 首批工具

### 文件系统工具

支持：

```text
filesystem.list_dir(path)
filesystem.read_file(path, range?)
filesystem.write_file(path, content)
filesystem.patch_file(path, patch)
```

约束：

- 所有路径必须在允许的 workspace root 内。
- `read_file` 支持范围读取。
- `write_file` 和 `patch_file` 需要 Policy 判断，默认中自主模式下需要确认。
- 第一版不支持删除文件。
- 第一版不支持 shell 命令执行。

风险等级：

- low：列目录，读取允许目录内的小范围文件。
- medium：写新文件，修改文件，大范围读取。
- high：删除、工作区外访问、shell 执行。第一版拒绝或不注册。

### 网页搜索工具

支持：

```text
web.search(query, recency?)
web.open_result(url)
web.extract_page(url)
web.summarize_sources(results)
```

约束：

- 只处理公开网页。
- 搜索和打开普通网页默认低风险。
- 不做登录、表单提交、上传、下载、支付、发消息。
- 外部网页内容视为不可信资料，不能改变 agent 指令、工具策略或权限。
- 搜索来源要结构化保存，支持管理员追溯。

## API 设计

```text
POST /api/runs
  创建 run
  body: goal, autonomy_level, enabled_tools, scopes

GET /api/runs
  列出所有 run，支持状态筛选

GET /api/runs/{id}
  获取 run 当前状态和摘要

GET /api/runs/{id}/events
  SSE 推送 run 事件

GET /api/runs/{id}/steps
  获取 run 步骤时间线

GET /api/runs/{id}/tool-calls
  获取工具调用详情

POST /api/approvals/{id}
  approve / deny / approve_for_run

POST /api/runs/{id}/input
  当 run 处于 needs_input 时补充信息

POST /api/runs/{id}/control
  pause / resume / cancel
```

SSE 事件：

```text
run_status_changed
step_started
model_decision
tool_call_requested
approval_required
tool_call_started
tool_call_finished
step_finished
artifact_created
run_completed
run_failed
```

## 管理员 Dashboard

Dashboard 默认展示所有 agent 执行细节。

推荐布局：

```text
左侧：
  Run List
  Filters
  Agent Config

中间：
  Run Timeline
  Decision Detail
  Tool Call Detail

右侧：
  Approval Queue
  Artifacts
  Audit Events
```

管理员可见字段：

- run goal、status、autonomy level、enabled tools、scopes。
- step index、type、status、reasoning summary、model decision raw。
- tool name、arguments、risk level、policy decision、approval status。
- tool result、error、duration。
- artifacts、sources、audit events。

## 升级路径

单用户本地版到多用户服务端版的升级路径：

1. `owners` 从默认 owner 变为真实用户表。
2. `Run Service` 增加认证和 owner 隔离。
3. `Memory Store` 从本地 SQLite 迁移到服务端数据库。
4. `Run Service` 增加队列，把长任务提交给 worker。
5. `Tool Executor` 拆为独立 worker，并增加执行沙箱。
6. `Approval Queue` 支持多用户、多角色和通知。
7. `Audit Log` 迁移为不可变事件流或集中日志系统。

由于 Agent Core、Policy Engine、Tool Registry 已经通过接口隔离，上述升级不需要重写核心决策协议。

## 测试策略

第一版测试重点：

- Policy Engine 单元测试：不同自主等级、工具风险、路径范围下的 allow / require_approval / deny。
- 文件系统工具测试：路径越界、范围读取、写入确认、patch 参数校验。
- 网页搜索工具测试：搜索结果结构、来源保存、外部内容不影响系统策略。
- Run Service 测试：状态迁移、等待审批、恢复执行、取消、失败收敛。
- Agent Core 协议测试：结构化输出解析、schema 校验失败、重试和 blocked。
- API 测试：创建 run、事件流、审批、控制命令。
- Dashboard 集成测试：run 列表、时间线、工具结果、审批队列展示。

## 第一版实现基线

为避免实现计划发散，第一版采用以下基线：

- 存储使用 SQLite。数据访问通过 `RunStore` 接口封装，未来可迁移到服务端数据库。
- LLM 调用抽象为 `LLMClient`，第一版实现继续复用当前 OpenAI 兼容 Chat Completions 接口。
- 网页搜索抽象为 `SearchProvider`，第一版通过配置选择具体 provider；业务代码只依赖统一搜索结果结构。
- 文件修改同时支持 `write_file` 和 `patch_file`。`write_file` 适合新文件或完整覆盖，`patch_file` 适合局部修改；两者默认都需要 Policy 判断。
- 管理员 Dashboard 优先实现 run 列表、时间线、工具调用详情、审批队列和审计日志；普通聊天页不保留。
