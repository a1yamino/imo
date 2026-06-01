# Agent MVP

这个 MVP 是一个单用户通用 agent 管理员 Dashboard。当前 runtime 通过内部 `RuntimeCommand` 驱动 deterministic mock loop，用来先跑通 run 生命周期、SQLite 持久化、runtime event、工具调用记录和审计展示。

## 启动

Agent SQLite 数据库默认写入当前目录的 `agent.db`。可以用 `AGENT_DB_PATH` 改位置：

```bash
export AGENT_DB_PATH=/tmp/imo-agent.db
```

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
- 查看所有 run 列表。
- 查看 run 状态、自主等级、启用工具和 workspace scope。
- 查看执行时间线。
- 查看每步 `reasoning_summary` 和模型决策 JSON。
- 查看 mock 工具调用参数、Policy 决策、风险等级和结果。
- 查看审计事件。
- 通过 SSE 观察 runtime event，并自动刷新当前选中的 run。

当前 mock run 会执行：

1. `queued -> running`
2. 生成一个 `filesystem.list_dir` 的结构化模型决策。
3. 通过 Policy Engine 判断低风险工具调用。
4. 保存 mock 工具结果。
5. 保存 observation step。
6. 保存 response step。
7. `running -> completed`

## API

创建并启动 run：

```bash
curl -s http://localhost:8080/api/runs \
  -H 'Content-Type: application/json' \
  -d '{
    "goal": "检查当前项目结构并记录观察结果",
    "autonomy_level": "medium",
    "enabled_tools": ["filesystem.list_dir", "web.search"],
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
- `internal/agent/service.go`：runtime command 消费、mock loop、runtime event 发布。
- `internal/webapp/server.go`：配置加载、路由注册和静态页面嵌入。
- `internal/webapp/agent_api.go`：admin 页面和 run API。
- `internal/webapp/assets/agent_admin.html`：管理员 Dashboard。
- `docs/superpowers/plans/2026-06-01-general-agent-mvp.md`：实现计划。
- `docs/superpowers/specs/2026-06-01-general-agent-architecture-design.md`：架构设计。

## 下一步

建议下一步按这个顺序接真实能力：

1. 实现真实 `filesystem.list_dir` 和 `filesystem.read_file`。
2. 把 mock tool call 替换成 Tool Registry + Tool Executor。
3. 增加 `waiting_approval` 状态和审批 API。
4. 接入网页搜索 provider。
5. 将 Agent Core 从 mock 决策替换为结构化 LLM 决策。
