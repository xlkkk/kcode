# kcode 游戏开发 Agent 设计文档

## 一、项目定位

将 kcode 改造为**专门开发棋牌类小游戏的 agent**,聚焦**服务器后端**,无客户端/引擎。

核心目标:**提效游戏开发,规范流程**。agent 必须强制按照既定流水线开发,不允许跳步。

输入输出链路:策划案读自**飞书文档** → 中间文档(`docs/flow.md` / `docs/modules.md`)复制入代码仓库 → **最终产物为游戏代码** → QA 文档(`docs/qa_cases.md`)写回飞书。

## 二、开发流程定义(五阶段流水线)

```
飞书策划案 ──读取──▶ 流程梳理 ──产出 docs/flow.md──▶ 模块划分 ──产出 docs/modules.md──▶ 【人工审核门】
                                                                        │ 通过
   QA案例生成 ◀── 自测(go test 全绿) ◀── 代码生成 ◀───────────────────────┘
 (产出 docs/qa_cases.md,写回飞书)         (游戏代码 = 最终产物)

附:docs/flow.md 与 docs/modules.md 最终复制到代码仓库
```

0. **输入源** — 游戏策划案从**飞书文档**读取(agent 通过 `read_feishu_doc` 获取内容)
1. **游戏流程梳理** — 产出 `docs/flow.md`:玩法规则、房间流程、结算逻辑、边界情况
2. **模块划分** — 产出 `docs/modules.md`:模块职责、接口、依赖。**此阶段需人工审核**,审核通过后才能进入代码生成
3. **代码生成** — 按审核通过的模块文档逐模块实现。**生成的游戏代码是最终产物**
4. **自测** — 跑 `go test` + 模拟对局,全绿才可推进
5. **QA 测试案例生成** — 产出 `docs/qa_cases.md`:用例表(前置条件/步骤/预期),**生成后写回飞书,非最终产物**

文档流转:`docs/flow.md` 与 `docs/modules.md` 最终复制到代码仓库;`docs/qa_cases.md` 写回飞书。

## 三、强制流程的总体设计:状态机 + 三道防线

核心结论:**光靠 prompt 约束不了 LLM,必须把流程做成"状态机 + 中间件门禁 + 人工审核门"的架构级硬约束**。

每个阶段有「准入条件 / 产出物 / 退出条件」,由三道防线保证强制:

| 防线 | 机制 | 强度 |
|---|---|---|
| 工具矩阵门禁 | 新中间件 `WorkflowGateMW` 拦截 `WrapInvokableToolCall`,按当前阶段放行/阻断工具 | 硬约束,不可绕过 |
| 阶段推进校验 | 阶段切换只能通过 `advance_stage` 工具,内部校验"产出物存在 + 审核已通过" | 硬约束 |
| 阶段上下文注入 | 复用 ReminderMW 模式,每轮注入"当前阶段 + 禁止事项" | 软约束,防 LLM 迷路 |

**人工审核闭环(唯一人工节点)**:模块文档产出 → 门禁强制触发 `request_review` → 用户通过/驳回 → 驳回意见写回状态机,阶段停在模块划分,修改后重新提交。代码生成工具在驳回期间持续被阻断。

## 四、ReAct 机制背景(门禁为何做在 Middleware 层)

kcode 的 ReAct 循环由 Eino 框架的 `ChatModelAgent` 内置,项目本身不重写循环,只通过 Middleware 挂钩子:

```
每次迭代:
  BeforeModelRewriteState → ChatModel.Generate → AfterModelRewriteState
    → 有 tool_calls? ──否──▶ 结束循环
        └─是──▶ WrapInvokableToolCall → 工具结果回流 → 下一轮
```

因此**工作流门禁只能做在 Middleware 层**,四个现成挂点正好够用:

| 挂点 | 工作流用途 |
|---|---|
| `BeforeModelRewriteState` | 注入"当前阶段 + 禁止事项" |
| `WrapInvokableToolCall` | 阶段工具矩阵门禁(rejected 结果回流给 LLM 让其改方向) |
| `AfterModelRewriteState` | 检测产出物、触发审核状态检查 |
| `BeforeAgent` | 会话启动时恢复持久化的阶段状态 |

这与现有 PermissionMW 同一套路,符合"不重写循环,只写 Middleware"的既定架构哲学。

## 五、具体改造落点

### 1. 新增 `internal/workflow/` 包(核心)✅ 已实现

- **workflow.go** — 类型与状态机:
  - `Stage` 枚举:`init → flow_analysis → module_design → code_gen → self_test → qa_cases → done`
  - `WorkflowState`:当前阶段、各阶段产出物清单、审核状态(`none/pending/approved/rejected` + 驳回意见)、PlanLoaded(策划案已读取标记)
  - `WorkflowDef`:✅ 已实现 YAML 化(config.yaml workflow 段:require_plan/review_stages/require_build/require_test;审核点当前仅 module_design)
- **gate.go** — 纯函数:`CanUseTool(stage, toolName, path)`、`CanAdvance(state, next)`、`VerifyTransition`(含 go build/go test 实跑校验),遵循项目"逻辑包纯函数 + Middleware 编排"的既有架构模式
- **store.go** — 状态持久化到 `.kcode/workflow.json`,会话中断后重开仍在原阶段断点续跑
- **gate_test.go** — 11 组单测:工具矩阵(含飞书)、审核门/RequirePlan/审核点开关、阶段推进、真实 go build/test 校验、StageNodes

### 2. 新增 `internal/middleware/workflow_gate.go`(门禁执行层)✅ 已实现

参照 HooksMW 的 rejected 阻断模式:

- `WrapInvokableToolCall`:未到代码生成阶段,`write_file`/`edit_file`/写类 `bash` 直接 rejected,返回"当前处于 X 阶段,禁止写代码"
- `BeforeModelRewriteState`:注入当前阶段上下文和退出条件
- 特判:模块文档产出且 `approved == false` 时,强制注入"必须调用 request_review 提交审核"

### 3. 新增 `internal/tools/workflow_tools.go`(三个工具)✅ 已实现

- **request_review** — 人工审核门,复用 AskUserTool 的 channel 机制:模块文档推给用户,通过/驳回意见写回 WorkflowState
- **advance_stage** — 推进阶段,内部调 `gate.CanAdvance` 校验,LLM 无法自行跳阶段
- **workflow_status** — 查看当前阶段、产出物、审核状态

### 4. 飞书集成(策划案输入 / QA 文档写回)✅ 已实现(client 模式)

- `internal/feishu/feishu.go` — OpenAPI 直连客户端(不引第三方 SDK):tenant_access_token 鉴权、ReadDocRaw(文档纯文本)、CreateDoc(新建文档+文本块写回)
- `internal/tools/feishu_tools.go`:
  - `read_feishu_doc` — 读取飞书策划案(URL 或 token),成功读取置位 `PlanLoaded`
  - `write_feishu_doc` — 把 `docs/qa_cases.md` 写回飞书(新建文档,返回链接)
- 门禁强制:`require_plan: true` 时未读策划案禁止推进;`write_feishu_doc` 仅 qa_cases/done 阶段放行(其余阶段被 WorkflowGateMW 拦截)
- 配置:`config.yaml` 的 `feishu` 段(app_id/app_secret/base_url,私有化可覆盖域名)

### 5. 修改既有文件 ✅ 已实现(计划 3 处,实际另加 web/session.go 接线)

- `internal/agent/agent.go` — `NewAgent` 新增 WorkflowState/WorkflowStore 选项,在 Agent 工具定格之后注册三个工作流工具(Lead 专属,子 Agent 不可见)
- `internal/middleware/registry.go` — 在 PermissionMW 之前插入 WorkflowGateMW(门禁先于权限,拦截更靠外)
- `internal/config/config.go` — 加 `workflow:` 配置段,加载方式对齐现有 hooks 的 YAML 三层合并
- `internal/web/session.go` — 计划外新增:workflow.enabled 时加载状态、组装 deps.Workflow、追加工作流系统提示

### 6. Skill 配合(产出物格式统一)⏳ 未实现

每个阶段配一个 skill 提供输出模板:

- `game-flow-analysis` → `docs/flow.md`
- `module-breakdown` → `docs/modules.md`(人工审核对象)
- `codegen-guide` → 按审核通过的模块文档逐模块生成
- `self-test-guide` → 跑 go test + 模拟对局
- `qa-case-writer` → `docs/qa_cases.md`

## 六、各环节准入条件 / 产出物 / 退出条件明细

### 总览表

| 环节 | 准入条件 | 产出物 | 退出条件 |
|---|---|---|---|
| 0 读取策划案 | 用户提供飞书文档链接/token | 策划案内容 + 本地快照 | 读取成功 + 关键要素齐全 |
| 1 流程梳理 | 策划案已读取 | `docs/flow.md` | 文档产出 + 结构完整 |
| 2 模块划分 | `flow.md` 已产出 | `docs/modules.md` | 文档产出 + **人工审核通过** |
| 3 代码生成 | 审核通过 | `src/**` 游戏代码(**最终产物**) | `go build` 编译通过 |
| 4 自测 | 编译通过 | 测试代码 + 测试报告 | `go test` 全绿 |
| 5 QA 案例 | 自测全绿 | `docs/qa_cases.md` | 文档产出 + **写回飞书成功** |

### 各环节明细

**环节 0:读取策划案(输入源)**

- 准入条件:会话启动,用户提供飞书文档链接/token
- 产出物:策划案内容读入上下文,落盘快照(如 `.kcode/workflow/plan.md`,防止飞书内容中途变更)
- 允许的工具:`read_feishu_doc` + 只读工具
- 退出条件:`read_feishu_doc` 调用成功 + 关键要素齐全(玩法规则、房间流程、结算规则、数值/概率配置);要素缺失时用 `AskUserQuestion` 向用户确认补全,**未读成功禁止推进**

**环节 1:游戏流程梳理**

- 准入条件:策划案已成功读取
- 产出物:`docs/flow.md` — 玩法规则、房间状态机(等待→发牌→出牌→结算)、消息流、结算逻辑、边界情况(断线/托管/异常退出)
- 允许的工具:只读 + 写 docs/
- 退出条件:`flow.md` 产出且结构完整(按 skill 模板逐章校验)

**环节 2:模块划分**

- 准入条件:`flow.md` 已产出
- 产出物:`docs/modules.md` — 模块清单(职责/接口/依赖)、数据模型、协议设计
- 允许的工具:只读 + 写 docs/ + `request_review`
- 退出条件:`modules.md` 产出 + **人工审核通过**

#### 人工审核门(环节 2 → 3 之间的门,非独立阶段)

- 触发:`modules.md` 产出后,门禁检测到 `approved == false` 持续注入"必须提交审核",倒逼 LLM 调 `request_review`
- 通过:`approved = true`,解锁代码生成工具
- 驳回:意见写回 `WorkflowState`,阶段停在模块划分,修改后重新提交
- 期间门禁:`src/` 一切写操作持续被 `WorkflowGateMW` 阻断

**环节 3:代码生成**

- 准入条件:审核通过(`approved == true`)
- 产出物:游戏服务端代码(`src/**`)——**最终产物**,严格对应 `modules.md` 模块清单
- 允许的工具:读写 src/,禁改 docs/
- 退出条件:`go build` 编译通过(可加 `go vet`)

**环节 4:自测**

- 准入条件:编译通过
- 产出物:测试代码(`*_test.go`)+ 测试报告(go test 输出、模拟对局结果)
- 允许的工具:写测试 + 跑 bash 测试命令
- 退出条件:`go test` 全绿;关键路径覆盖(规则算法全枚举用例、金币流水守恒)

**环节 5:QA 测试案例生成**

- 准入条件:自测全绿
- 产出物:`docs/qa_cases.md` — 用例表(前置条件/步骤/预期)
- 允许的工具:只读 + 写 docs/ + `write_feishu_doc`
- 退出条件:文档产出 + **写回飞书成功** → 流程终态

### 收尾动作(流程终态后)

- `docs/flow.md` 与 `docs/modules.md` 复制进代码仓库(随代码提交)
- `docs/qa_cases.md` 只写回飞书,不强制入库

### 全局规则

- 推进唯一入口:`advance_stage` 内部校验退出条件,LLM 无法自行跳阶段
- 工具矩阵:每环节一套允许/禁止工具集,由 `WorkflowGateMW` 硬拦截
- 断点续跑:状态持久化到 `.kcode/workflow.json`,会话中断重开后停在原环节

> **最终产物是游戏代码**(环节 3 产出);QA 文档只是质量交付物。

## 七、实施路线图

1. **状态机 + 门禁中间件**:`internal/workflow` + `workflow_gate.go`,验证"阶段 1 调 write_file 被拒" — ✅ 已完成
2. **人工审核门**:`request_review` + ReviewCh 打通,验证驳回 → 修改 → 重新提交闭环 — ✅ 已完成(升级为专用审核链路)
3. **5 个阶段 skill**:统一产出物模板 — ⏳ 未开始
4. **持久化 + 断点恢复**:workflow.json 落盘,重开会话验证 — ✅ 已完成
5. **流程定义下沉到 config.yaml**:做成项目级配置 — ✅ 已完成(require_plan/review_stages/require_build/require_test)
6. **飞书接入**:read_feishu_doc / write_feishu_doc,打通策划案读取与 QA 写回闭环 — ✅ 已完成(client 模式)

## 八、待决策事项

- ~~飞书接入方式~~ — ✅ 已决策:client 模式(OpenAPI 直连)
- 具体玩法品类(斗地主/麻将/德州等)与对应 skill 库内容
- 技术栈确认(默认 Go 后端 + WebSocket + protobuf + Redis/MySQL)
- 分发形态(纯配置包 / kcode-game 发行版 / 渐进式)

## 九、遗留问题(技术债 / 未覆盖项 / 风险点)

### 流程机制缺口

| # | 问题 | 说明 | 影响 |
|---|---|---|---|
| 1 | qa_cases → done 无退出条件 | `CanAdvance` 对 done 直接放行,写回飞书仅靠系统提示软约束 | 未写回飞书也能完结流程 |
| 2 | 产出物只校验存在性 | flow.md/modules.md 仅查文件存在,未做结构完整性校验(依赖 5 个阶段 skill 模板,未实现) | 空文档也能通过退出条件 |
| 3 | 环节 0 要素齐全校验缺失 | 只有 PlanLoaded 布尔,未校验玩法规则/房间流程/结算规则/数值配置等关键要素,缺失时 AskUserQuestion 补全闭环未实现 | 读了一份无关文档也算"已读" |
| 4 | 策划案快照未落盘 | ReadDocRaw 内容只进上下文,无 `.kcode/workflow/plan.md` 快照 | 飞书中途变更无法追溯,断点续跑后需重读 |
| 5 | 阶段只能前进 | 无 prev_stage/重置机制;驳回只停留原阶段,无法回退到 flow_analysis | 流程走错只能重开工作流 |
| 6 | 审核点是单值状态 | Review 是单一字段,多审核点需改 map[Stage]ReviewStatus(Validate 已限制 module_design) | 无法配置多个人工审核点 |
| 7 | 阶段文案硬编码 | StageDescriptions 未随 WorkflowDef 动态生成,配置移除审核点后仍提示"必须提交人工审核" | 配置与提示不一致 |

### 门禁强制力风险

| # | 问题 | 说明 | 影响 |
|---|---|---|---|
| 8 | bash 不受门禁 | CanUseTool 只拦 write_file/edit_file,LLM 可 bash 重定向写文件绕过矩阵 | 强制力依赖 PermissionMW 兜底 |

### 飞书客户端健壮性

| # | 问题 | 说明 | 影响 |
|---|---|---|---|
| 9 | feishu 包无单测 | extractDocToken(自定义域名/share 链接)、token 缓存续期、HTTP 错误路径未覆盖 | 边界回归无保障 |
| 10 | markdown 保真缺失 | CreateDoc 按行拆纯文本块,标题/表格/代码块格式丢失;raw_content 读回同样丢结构 | QA 文档写回飞书后排版退化 |
| 11 | 写回无重试/幂等 | CreateDoc 失败即返回错误,无重试;写回结果未记录进 state | 网络抖动导致流程卡在 qa_cases |

### 交互健壮性

| # | 问题 | 说明 | 影响 |
|---|---|---|---|
| 12 | 审核请求无超时 | 用户不响应时 request_review 阻塞整轮,仅连接断开/abort 解除 | 挂起轮次无法推进 |
| 13 | workflow_status 不显示 plan_loaded | 状态输出缺策划案已读标记 | LLM 判断"是否已读策划案"靠猜 |

## 十、实现状态(截至 2026-10-05)

| 模块 | 状态 | 文件 |
|---|---|---|
| 状态机与类型定义 | ✅ | internal/workflow/workflow.go |
| 门禁纯函数 + 编译/测试实跑校验 | ✅ | internal/workflow/gate.go |
| 状态持久化(断点续跑) | ✅ | internal/workflow/store.go |
| 门禁中间件 WorkflowGateMW | ✅ | internal/middleware/workflow_gate.go |
| 工作流工具(request_review / advance_stage / workflow_status) | ✅ | internal/tools/workflow_tools.go |
| 飞书客户端(client 模式) | ✅ | internal/feishu/feishu.go |
| 飞书读写工具 | ✅ | internal/tools/feishu_tools.go |
| 单元测试(11 组) | ✅ | internal/workflow/gate_test.go |
| 配置段 workflow/feishu + 三层合并 | ✅ | internal/config/config.go |
| 门禁注册(先于 PermissionMW) | ✅ | internal/middleware/registry.go |
| 工具注册(Agent 定格后,Lead 专属) | ✅ | internal/agent/agent.go |
| 装配接线 + 工作流系统提示 | ✅ | internal/web/session.go |
| 审核事件协议(review_request/review_response) | ✅ | internal/web/messages.go |
| 配置示例 | ✅ | .kcode/config.yaml.example |
| 流程定义 YAML 化(WorkflowDef) | ✅ | require_plan/review_stages/require_build/require_test;审核点当前仅 module_design |
| 5 个阶段 skill(产出物模板) | ⏳ | .kcode/skills/ |
| 专用审核前端面板(渲染) | 🚧 | 后端契约已就绪,前端 SPA 待实现(仓库外) |

**验证结果**:`go build ./...`、`go vet`、`gofmt` 全部通过;`go test ./internal/workflow/` 11 组全绿(含 RequirePlan/审核点开关/实跑校验开关/StageNodes/飞书工具矩阵)。

**强制闭环运转路径**:用户提供策划案链接 → `read_feishu_doc`(置位 PlanLoaded,require_plan 时强制)→ `advance_stage` → 写 docs/flow.md(写代码被拒)→ `advance_stage` → 写 docs/modules.md → 门禁挂审核提示 → `request_review`(专用面板:文档预览 + 加粗当前流程节点 + 通过/驳回意见)→ 驳回停留修改重提 / 通过 → `advance_stage`(go build 实跑)→ code_gen → `advance_stage`(go test 实跑)→ self_test → qa_cases → `write_feishu_doc` 写回飞书 → done。

### 审核面板前端契约(仓库外 SPA 实现参考)

- 下行 `review_request`: `{ id, artifact_path, artifact_content, stages[] }`;`stages[i] = { name, desc, current, done }`
- 渲染规则:**流程节点横排,`current: true` 的节点加粗高亮**,`done: true` 置灰/打勾,其余正常色
- 面板主体渲染 `artifact_content`(markdown 预览),底部「通过 / 驳回」按钮;驳回时弹意见输入框
- 上行 `review_response`: `{ id, decision: "approve"|"reject", feedback? }`
