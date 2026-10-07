# kcode

基于字节跳动 [Eino ADK](https://github.com/cloudwego/eino) 框架的 AI 编程助手（Code Agent）。

与通用 coding agent 不同,kcode 聚焦**棋牌类小游戏服务器后端**的开发:通过「状态机 + 中间件门禁 + 人工审核门」强制 agent 按五阶段流水线开发,提效并规范游戏开发流程。方案设计见 [game.md](game.md)。

## 功能特性

### Agent 核心

- **不重写 ReAct Loop**:直接使用 Eino `ChatModelAgent` 内置循环,业务能力全部以 `ChatModelAgentMiddleware` 注入,横切关注点与循环解耦
- **内置工具集**:读/写/编辑文件、执行命令、文件搜索、内容检索、延迟工具检索,以及工作流、飞书、MCP 等扩展工具
- **中间件链**:权限检查、上下文压缩、Hooks、会话持久化、system-reminder 注入、工具结果预算、文件快照恢复

### 强制五阶段开发流程(核心特性)

```
读取策划案 → 流程梳理 → 模块划分(人工审核) → 代码生成 → 自测 → QA 案例
  (飞书)    flow.md    modules.md   审核门   游戏代码   go test   qa_cases.md
                                                             (写回飞书)
```

- **状态机**:`init → flow_analysis → module_design → code_gen → self_test → qa_cases → done`,状态持久化到 `.kcode/workflow.json`,会话中断可断点续跑
- **工具矩阵门禁**(`WorkflowGateMW`):未到代码生成阶段,一切写代码操作被中间件硬拦截,无法绕过
- **阶段推进校验**:推进唯一入口是 `advance_stage` 工具,内部实跑 `go build` / `go test` 校验退出条件
- **人工审核门**:`modules.md` 产出后强制提交 `request_review`,驳回则停留原阶段修改重提
- **流程定义 YAML 化**:`require_plan` / `review_stages` / `require_build` / `require_test` / `require_simulation` 均可配置

### 安全与可控

- **五层权限防御**:安全命令 → 危险命令检测 → OS 沙箱(macOS sandbox-exec / Linux seccomp)→ 规则引擎 → 模式策略;危险操作通过 Interrupt 机制等待用户确认
- **路径沙箱**:工作目录外的路径读写被拒绝
- **上下文压缩**:token 超阈值时自动生成摘要压缩历史,支持长会话
- **工具结果预算**:超大结果溢写磁盘,控制单轮上下文膨胀

### 协作与扩展

- **多 Agent / Teams**:子 Agent(lead 专属工具可见性控制)、in-process 团队协作、任务分发
- **Git worktree 隔离**:为子 Agent / 队友创建独立工作副本
- **MCP 协议**:支持 stdio 与远程 URL 两种方式接入外部工具
- **长期记忆**:用户级 + 项目级双目录,记忆提取与整合(consolidation)
- **技能系统**:Markdown 定义技能,内置技能 + 本地 `.kcode/skills/` 加载
- **飞书集成**(client 模式):策划案从飞书文档读取(`read_feishu_doc`),QA 案例写回飞书(`write_feishu_doc`)
- **Web UI**:WebSocket + 静态 SPA,含会话管理、专用人工审核面板、轮次中止

## 架构概览

```
cmd/kcode          入口层:CLI 解析、配置加载、模式分发
internal/agent     Agent 装配层:ChatModelAgent + Middleware 链 + Runner
internal/middleware 中间件层(核心):权限/压缩/Hooks/会话/门禁/预算/恢复
internal/tools     工具层:Eino InvokableTool 实现
internal/workflow  工作流状态机、门禁纯函数、持久化
internal/web       Web 服务(WebSocket / 会话 / 消息协议)
internal/{agents,teams,memory,skills,mcp,feishu,sandbox,worktree,...}  基础设施
```

## 快速开始

### 依赖

- Go 1.25+
- 一个 LLM Provider 的 API Key

### 配置

创建 `.kcode/config.yaml`(参考 [.kcode/config.yaml.example](.kcode/config.yaml.example)):

```yaml
providers:
  - name: deepseek
    protocol: openai-compat
    base_url: https://api.deepseek.com/v1
    model: deepseek-chat
    context_window: 128000

# 工作流强制流程(游戏项目):开启后 agent 强制按五阶段流水线开发
workflow:
  enabled: true
  require_plan: false
  review_stages:
    - module_design
  require_build: true
  require_test: true
  require_simulation: true
```

- API Key **不写入配置文件**,运行时从环境变量读取:`export OPENAI_API_KEY=sk-xxx`(openai 及 openai-compat 协议)
- 配置文件查找顺序:`--config 指定路径` → `~/.kcode/config.yaml` → `./.kcode/config.yaml` → `./.kcode/config.local.yaml`
- 飞书集成需在 `feishu:` 段配置 `app_id` / `app_secret`,并置 `enabled: true`

### 构建与启动

```bash
make deps     # 拉取依赖
make build    # 构建到 bin/kcode(静态页面复制到 bin/static)
make test     # 运行测试
```

**Web 模式(当前唯一可用模式)**:

```bash
# 方式一:make(默认工作目录 GAME_DIR,可覆盖)
make run-web
make run-web GAME_DIR=/path/to/game-project

# 方式二:直接运行
./bin/kcode --web --port 8080 --dir /path/to/game-project
```

启动后访问 `http://localhost:8080`。

命令行参数:

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--web` | - | 启动 Web UI 服务(WebSocket + 静态 SPA) |
| `--port` | `8080` | Web 服务端口 |
| `--dir` | 当前目录 | 项目工作目录(workflow 状态、代码生成、沙箱均落在此目录) |
| `--static` | 自动探测 | 前端静态资源目录(依次探测 bin/static、web/static、static) |
| `--config` | 自动探测 | 配置文件路径 |

> CLI / TUI / print / remote 模式尚未实现(`cmd/kcode/main.go` 中 TODO),当前仅 `--web` 模式可用。

### 开发命令

```bash
make help        # 查看全部命令
make build       # 构建
make run-web     # Web 模式运行(可 GAME_DIR=xxx 覆盖工作目录)
make test        # 全部测试
make test-race   # 竞态检测
make test-cover  # 覆盖率报告
make fmt         # 格式化
make vet         # 静态分析
make lint        # golangci-lint(需预装)
make build-linux # 交叉编译 Linux amd64
make clean       # 清理构建产物
```

## 项目结构

```
cmd/kcode/          入口(main.go)
internal/
├── agent/          Agent 装配
├── middleware/     中间件层(permission/compaction/hooks/session/reminder/
│                   result_budget/recovery/workflow_gate)
├── tools/          工具实现(read_file/write_file/edit_file/bash/glob/grep/
│                   workflow_tools/feishu_tools/...)
├── workflow/       工作流状态机 + 门禁纯函数 + 持久化 + 单测
├── web/            Web 服务(ws/session/messages)
├── agents/         子 Agent 定义与加载
├── teams/          团队协作(in-process)
├── memory/         长期记忆 + 提取 + 整合
├── skills/         技能系统
├── mcp/            MCP 客户端与工具转换
├── feishu/         飞书 OpenAPI 客户端
├── permissions/    权限引擎
├── sandbox/        OS 沙箱(darwin/linux)
├── compact/        压缩算法
├── worktree/       git worktree 隔离
├── todo/           Todo 管理
└── ...
docs/               工作流产出物示例:flow.md(流程梳理)、modules.md(模块划分)、qa_cases.md(QA 案例)
.kcode/             运行态配置与状态:config.yaml、workflow.json、skills/
web/static/         Web UI 静态页面
```

## 文档索引

| 文档 | 内容 |
|------|------|
| [beizhu.md](beizhu.md) | 基于 Eino ADK 的架构设计(分层、中间件、数据流) |
| [game.md](game.md) | 五阶段强制开发流程设计(状态机、门禁、路线图、遗留问题) |
| [docs/flow.md](docs/flow.md) | 工作流产出物示例:斗地主流程梳理 |
| [docs/modules.md](docs/modules.md) | 工作流产出物示例:斗地主模块划分 |
| [docs/qa_cases.md](docs/qa_cases.md) | 工作流产出物示例:QA 测试案例 |
| [eino.md](eino.md) | Eino 概念速记(state / middleware / callback) |
| [mcp.md](mcp.md) | MCP 集成流程说明 |
