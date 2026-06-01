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
- 查看工具调用参数、Policy 决策、风险等级和结果。当前普通 AI 对话不会产生工具调用。
- 查看审计事件。
- 通过 SSE 观察 runtime event，并自动刷新当前选中的 run。

当前 AI 对话 run 会执行：

1. `queued -> running`
2. 按 `session_id` 读取之前已完成 run 的 user/assistant 历史。
3. 调用 OpenAI 兼容 Chat Completions。
4. 保存 response step。
5. `running -> completed`

## API

创建并启动 run：

```bash
curl -s http://localhost:8080/api/runs \
  -H 'Content-Type: application/json' \
  -d '{
    "goal": "检查当前项目结构并记录观察结果",
    "autonomy_level": "medium",
    "enabled_tools": [],
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
    "enabled_tools": [],
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
- `internal/agent/types.go`：agent run、step、tool call、audit、policy、event 类型。
- `internal/agent/policy.go`：可配置自主等级的最小 Policy Engine。
- `internal/agent/store.go`：SQLite schema 和持久化查询。
- `internal/agent/service.go`：runtime command 消费、多轮 session 上下文组装、AI 对话 runtime、runtime event 发布。
- `internal/agent/llm.go`：OpenAI 兼容 Chat Completions client。
- `internal/webapp/server.go`：配置加载、路由注册和静态页面嵌入。
- `internal/webapp/agent_api.go`：admin 页面和 run API。
- `internal/webapp/assets/agent_admin.html`：管理员 Dashboard。
- `docs/superpowers/plans/2026-06-01-general-agent-mvp.md`：实现计划。
- `docs/superpowers/specs/2026-06-01-general-agent-architecture-design.md`：架构设计。

## 下一步

建议下一步按这个顺序接真实能力：

1. 增加 Tool Registry + Tool Executor。
2. 增加 `waiting_approval` 状态和审批 API。
3. 实现真实 `filesystem.list_dir` 和 `filesystem.read_file`。
4. 接入网页搜索 provider。
