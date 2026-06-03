# Agent MVP

这个 MVP 是一个单用户通用 agent 管理员 Dashboard。当前 runtime 通过内部 `RuntimeCommand` 驱动普通 AI 对话：一次用户发言会创建一个 run，同一轮对话共享 `session_id`，runtime 会把该 session 下的历史 user/assistant 消息组装后调用 OpenAI 兼容 Chat Completions，把回复写入时间线，并通过 runtime event 通知 Dashboard 观察状态变化。

## 启动

Agent SQLite 数据库默认写入当前目录的 `agent.db`。可以用 `AGENT_DB_PATH` 改位置：

```bash
export AGENT_DB_PATH=/tmp/imo-agent.db
```

AI 对话 run 需要 OpenAI 兼容配置：

```bash
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://api.openai.com
export OPENAI_MODEL=gpt-4o-mini
```

服务启动不强制要求 `OPENAI_API_KEY`，但缺少 key 时新建的 AI 对话 run 会进入 `failed`。

网络搜索 provider 可配置。当前支持 Serper.dev 搜索：

```bash
export WEB_SEARCH_PROVIDER=serper
export SERPER_API_KEY=...
```

`web.fetch` 是独立 HTTP 抓取工具，不依赖 Serper；`web.search` 在 `WEB_SEARCH_PROVIDER=serper` 且配置 `SERPER_API_KEY` 时注册。

启动：

```bash
go run .
```

默认地址：

```text
http://localhost:8080
```

管理员 Dashboard：

```text
http://localhost:8080/admin
```

## 当前能力

Dashboard 支持：

- 创建 agent run。
- 通过 `session_id` 支持多轮对话；每次发言是一个独立 run，但会继承同一 session 的历史上下文。
- 按 session 聚合查看 run 列表，并在每个 session 下展开具体 run。
- 查看 run 状态、自主等级、启用工具和 workspace scope。
- 查看执行时间线。
- 查看每步 `reasoning_summary` 和模型决策 JSON。
- 查看工具调用参数、Policy 决策、风险等级和结果。
- 查看审计事件。
- 通过 Stream 开关发送 `/stream on` 或 `/stream off`，切换当前 session 的 LLM streaming 请求模式。
- 通过 SSE 观察 runtime event；stream 模式下文本 delta 会实时追加到当前 Assistant 消息，完成后再用 snapshot 校准。

当前 AI run 会执行：

1. `queued -> running`
2. 如果用户输入是 runtime 斜杠命令，runtime 直接消费命令、写入审计和 response step，不调用 LLM。
3. 普通对话按 `session_id` 读取之前已完成 run 的 user/assistant 历史。
4. 读取 session runtime options，例如 `stream`，并把当前 run 启用的工具转换成 Chat Completions `tools` 参数传给 LLM。
5. 如果 LLM 通过 provider-native `message.tool_calls` 请求工具，runtime 保存 model decision step，执行工具，保存 tool call 和 observation step，并用标准 `role=tool` / `tool_call_id` 消息把结果交回模型。
6. 如果决策是 `final` 或普通文本，保存 response step。
7. `running -> completed`

## Runtime 斜杠命令

斜杠命令是 agent runtime 的控制输入，不是前端私有状态。Dashboard 的 Stream 按钮只是发送同样的命令：

```text
/stream on
/stream off
/stream
```

- `/stream on`：把当前 session 的 `stream` runtime option 设为 `true`。
- `/stream off`：把当前 session 的 `stream` runtime option 设为 `false`。
- `/stream`：读取当前 session 的 stream 状态。

stream option 存在 SQLite 的 `session_runtime_options` 表里，作用域是 session。普通对话 run 在调用 LLM 前读取该 option；当 `stream=true` 时，OpenAI 兼容 client 会发送 `"stream": true`。文本 delta 会通过 `llm_response_delta` runtime event 推给 Dashboard，前端即时追加显示；runtime 仍会聚合完整回复并在完成时写入 response step，用于审计和最终 snapshot 校准。工具调用 delta 当前仍在后端聚合成完整 `tool_calls` 后执行。

当前已注册的只读工具：

- `filesystem.list_dir`：列出 `workspace_scope` 内目录。
- `filesystem.read_file`：读取 `workspace_scope` 内小于 1MiB 的文本文件。
- `web.fetch`：读取 HTTP(S) 页面，返回 title、description、正文文本，默认最多 12000 字符。
- `web.search`：通过配置的搜索 provider 搜索网页。当前 provider 支持 `serper`，请求 Serper 的 `https://google.serper.dev/search`。

工具路径必须是相对路径，不能逃逸 `workspace_scope`。

工具调用主路径使用 OpenAI 兼容 Chat Completions 的 native tool calling：

- 请求 LLM 时传 `tools` 和 `tool_choice: "auto"`。
- 内部工具名如 `filesystem.list_dir` 会在 API 层映射成函数名 `filesystem__list_dir`，收到 provider 返回后再映射回内部工具名执行。
- LLM 返回 `message.tool_calls` 时，runtime 执行对应工具。
- 工具结果通过 `role: "tool"`、`tool_call_id` 和 JSON content 追加回下一次 LLM 请求。
- 如果 provider 不支持 native tools，runtime 仍保留文本 JSON `tool_call` 解析作为兼容 fallback，但这不是首选路径。

最终回复格式：

```json
{"type":"final","content":"I found README.md.","reasoning_summary":"The listing contains README.md."}
```

如果模型返回普通文本，runtime 会把它当作最终回复。

## API

创建并启动 run：

```bash
curl -s http://localhost:8080/api/runs \
  -H 'Content-Type: application/json' \
  -d '{
    "goal": "检查当前项目结构并记录观察结果",
    "autonomy_level": "medium",
    "enabled_tools": ["filesystem.list_dir", "filesystem.read_file", "web.search", "web.fetch"],
    "workspace_scope": "."
  }'
```

继续同一轮对话时带上前一次返回的 `session_id`：

```bash
curl -s http://localhost:8080/api/runs \
  -H 'Content-Type: application/json' \
  -d '{
    "session_id": "<session_id>",
    "goal": "你刚才说了什么？",
    "autonomy_level": "medium",
    "enabled_tools": ["filesystem.list_dir", "filesystem.read_file", "web.search", "web.fetch"],
    "workspace_scope": "."
  }'
```

列出 run：

```bash
curl -s http://localhost:8080/api/runs
```

查看 run 快照：

```bash
curl -s http://localhost:8080/api/runs/<run_id>
```

查看整轮 session 快照：

```bash
curl -s http://localhost:8080/api/sessions/<session_id>
```

返回的 `runtime_options.stream` 表示该 session 当前是否启用 LLM streaming 请求模式。

查看细分资源：

```bash
curl -s http://localhost:8080/api/runs/<run_id>/steps
curl -s http://localhost:8080/api/runs/<run_id>/tool-calls
curl -s http://localhost:8080/api/runs/<run_id>/audit-events
```

观察 runtime event 的 SSE 输出：

```bash
curl -N http://localhost:8080/api/runs/<run_id>/events
```

## 代码结构

- `main.go`：根入口，只负责调用 Web 应用启动逻辑。
- `internal/agent/types.go`：agent run、step、tool call、audit、policy、event、LLM tool calling、session runtime option 类型。
- `internal/agent/policy.go`：可配置自主等级的最小 Policy Engine。
- `internal/agent/store.go`：SQLite schema、session runtime options 和持久化查询。
- `internal/agent/service.go`：runtime command 消费、斜杠命令处理、多轮 session 上下文组装、AI 对话 runtime、runtime event 发布。
- `internal/agent/tools.go`：Tool Registry 和只读 filesystem 工具。
- `internal/agent/web_tools.go`：Serper 搜索 provider 和独立 HTTP fetch 工具。
- `internal/agent/llm.go`：OpenAI 兼容 Chat Completions client，支持 native `tools/tool_calls`、普通 JSON 响应、streaming SSE delta 回调和最终聚合。
- `internal/webapp/server.go`：路由注册和静态页面嵌入。
- `internal/webapp/agent_api.go`：admin 页面和 run API。
- `internal/webapp/assets/agent_admin.html`：管理员 Dashboard。
- `docs/superpowers/plans/2026-06-01-general-agent-mvp.md`：实现计划。
- `docs/superpowers/specs/2026-06-01-general-agent-architecture-design.md`：架构设计。

## 下一步

建议下一步按这个顺序接真实能力：

1. 增加 `waiting_approval` 状态和审批 API。
2. 增加更多搜索 provider fallback。
3. 增加 tool-call schema 校验和更强的模型输出修复策略。
4. 增加上下文 token budget 和 session summary。
