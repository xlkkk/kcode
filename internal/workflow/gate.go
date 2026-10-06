// gate.go — 工作流门禁规则与阶段推进校验。
//
// 本包遵循「逻辑包纯函数 + Middleware 编排」的既有架构模式:
// CanUseTool 是工具矩阵纯函数,供 WorkflowGateMW 拦截写操作;
// CanAdvance / VerifyTransition 供 advance_stage 工具校验退出条件。
//
// 工具矩阵(写类工具 path × 阶段):
//   - init:             禁止一切写
//   - flow_analysis:    仅 docs/(flow.md)
//   - module_design:    仅 docs/(modules.md)
//   - code_gen/self_test:docs/ 外任意(docs/ 锁定)
//   - qa_cases:         仅 docs/qa_cases.md
//   - done:             全部放行
//
// 锁定规则:flow.md 仅在 flow_analysis 可写;modules.md 仅在 module_design 可写;
// qa_cases.md 仅在 qa_cases 可写。read 类工具与 bash 不做门禁(bash 交给 PermissionMW)。

package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 被门禁的写类工具名(与 tools 包的常量保持同值;workflow 不 import tools,
// 避免 tools/workflow_tools.go → workflow → tools 的循环依赖)。
const (
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"
)

// 飞书工具名(门禁只对 write_feishu_doc 限制阶段;read 任意阶段可调)。
const (
	ToolReadFeishuDoc  = "read_feishu_doc"
	ToolWriteFeishuDoc = "write_feishu_doc"
)

// WorkflowToolNames 是工作流自带的工具名(门禁始终放行)。
const (
	ToolRequestReview  = "request_review"
	ToolAdvanceStage   = "advance_stage"
	ToolWorkflowStatus = "workflow_status"
	ToolSimulateMatch  = "simulate_match"
)

// checkTimeout 是 VerifyTransition 里 go build / go test 的超时上限。
const checkTimeout = 5 * time.Minute

// CanUseTool 判断当前阶段是否允许调用某工具。
// path 是写类工具的目标路径参数(非写类工具传空串即可)。
// 返回 (允许?, 阻断原因)。
func CanUseTool(stage Stage, toolName, path string) (bool, string) {
	// read_feishu_doc 任意阶段可读(策划案可能在任意时刻补读)。
	if toolName == ToolReadFeishuDoc {
		return true, ""
	}
	// write_feishu_doc 仅 qa_cases/done 阶段可用:QA 案例写回飞书是流程收尾动作。
	if toolName == ToolWriteFeishuDoc {
		if stage != StageQACases && stage != StageDone {
			return false, "write_feishu_doc 仅在 qa_cases 阶段可用(当前 " + string(stage) + ")"
		}
		return true, ""
	}

	// simulate_match 仅 self_test 阶段可用:对局模拟是自测环节的收尾验证,
	// 其余阶段无游戏代码可模拟。
	if toolName == ToolSimulateMatch {
		if stage != StageSelfTest {
			return false, "simulate_match 仅在 self_test 阶段可用(当前 " + string(stage) + ")"
		}
		return true, ""
	}

	// 工作流工具与只读/其他工具不拦截:门禁只针对写文件。
	if toolName != ToolWriteFile && toolName != ToolEditFile {
		return true, ""
	}

	p := normalizePath(path)
	if p == "" {
		return false, "写文件必须提供有效 path 参数"
	}

	switch stage {
	case StageInit:
		return false, "当前处于 init 阶段,禁止一切写操作;先调用 advance_stage 进入 flow_analysis"

	case StageFlowAnalysis:
		if !underDocs(p) {
			return false, "flow_analysis 阶段只允许写 docs/ 目录(flow.md),禁止写代码文件"
		}
		if isArtifact(p, ArtifactModules) || isArtifact(p, ArtifactQACases) {
			return false, "flow_analysis 阶段只允许写 docs/flow.md"
		}
		return true, ""

	case StageModuleDesign:
		if !underDocs(p) {
			return false, "module_design 阶段只允许写 docs/ 目录(modules.md),禁止写代码文件"
		}
		if isArtifact(p, ArtifactFlow) {
			return false, "docs/flow.md 已锁定,module_design 阶段不可修改"
		}
		if isArtifact(p, ArtifactQACases) {
			return false, "docs/qa_cases.md 仅 qa_cases 阶段可写"
		}
		return true, ""

	case StageCodeGen, StageSelfTest:
		if underDocs(p) {
			return false, "code_gen/self_test 阶段 docs/ 目录已锁定,只允许写代码与测试文件"
		}
		return true, ""

	case StageQACases:
		if !isArtifact(p, ArtifactQACases) {
			return false, "qa_cases 阶段只允许写 docs/qa_cases.md"
		}
		return true, ""

	case StageDone:
		return true, ""

	default:
		return false, "未知阶段 " + string(stage)
	}
}

// CanAdvance 判断能否从当前阶段推进到 next(def 提供可配置的审核点与
// 策划案强制项)。不执行编译/测试(由 VerifyTransition 负责),只校验
// 产出物与审核状态。
func CanAdvance(def WorkflowDef, s *WorkflowState, next Stage) (bool, string) {
	if next != NextStage(s.Stage) {
		return false, "只能按顺序推进:当前 " + string(s.Stage) + ",目标必须是 " + string(NextStage(s.Stage))
	}
	if next == StageDone {
		return true, ""
	}

	_, hasFlow := s.Artifacts[StageFlowAnalysis]
	_, hasModules := s.Artifacts[StageModuleDesign]

	switch next {
	case StageFlowAnalysis:
		// 配置了 require_plan 时必须先读策划案(read_feishu_doc 置位 PlanLoaded)。
		if def.RequirePlan && !s.PlanLoaded {
			return false, "尚未读取策划案:先调用 read_feishu_doc 读取策划案文档"
		}
		return true, ""

	case StageModuleDesign:
		if !hasFlow {
			return false, "缺少产出物 docs/flow.md,必须先完成流程梳理"
		}
		return true, ""

	case StageCodeGen:
		if !hasModules {
			return false, "缺少产出物 docs/modules.md,必须先完成模块划分"
		}
		if def.HasReview(StageModuleDesign) && s.Review != ReviewApproved {
			return false, "模块划分未通过人工审核(当前状态 " + string(s.Review) + "),先调用 request_review 提交审核"
		}
		return true, ""

	case StageSelfTest:
		// 编译校验由 VerifyTransition 执行 go build。
		return true, ""

	case StageQACases:
		// 测试校验由 VerifyTransition 执行 go test;模拟校验见下:
		// RequireSimulation 时对局模拟必须达标(simulate_match 置位)。
		if def.RequireSimulation && !s.SimulationPassed {
			return false, "对局模拟未达标:先调用 simulate_match 完成概率与金币守恒验证(生成 simulation/main.go 并运行)"
		}
		return true, ""

	default:
		return false, "非法目标阶段 " + string(next)
	}
}

// VerifyTransition 校验推进到 next 的退出条件:先 CanAdvance,再对需要编译/
// 测试校验的跳转执行 go build / go test(def.RequireBuild/RequireTest 控制)。
// dir 是项目 workDir。返回 (通过?, 失败原因);失败原因用于回传给 LLM 修正。
func VerifyTransition(def WorkflowDef, s *WorkflowState, dir, next string) (bool, string) {
	nextStage := Stage(next)
	if ok, reason := CanAdvance(def, s, nextStage); !ok {
		return false, reason
	}

	switch nextStage {
	case StageSelfTest:
		if def.RequireBuild {
			if out, err := runGo(dir, "build", "./..."); err != nil {
				return false, "go build 未通过,不能进入自测阶段:\n" + out
			}
		}
	case StageQACases:
		if def.RequireTest {
			if out, err := runGo(dir, "test", "./..."); err != nil {
				return false, "go test 未全绿,不能进入 QA 案例阶段:\n" + out
			}
		}
	}
	return true, ""
}

// Advance 执行阶段推进:校验通过后更新状态。跨过模块划分审核点后清空
// 审核状态与驳回意见(审核只约束 module_design → code_gen)。
// 返回 (新阶段, 错误)。
func Advance(def WorkflowDef, s *WorkflowState, dir string) (Stage, error) {
	next := NextStage(s.Stage)
	if next == "" {
		return "", errDone
	}
	if ok, reason := VerifyTransition(def, s, dir, string(next)); !ok {
		return "", &AdvanceError{Reason: reason}
	}
	s.Stage = next
	RecordTimeline(s, next, EventStageEntered, StageDescriptions[next])
	if IsBefore(StageModuleDesign, next) {
		s.Review = ReviewNone
		s.ReviewFeedback = ""
	}
	return next, nil
}

// DetectArtifacts 扫描 workDir 下三大产出物,更新 state.Artifacts。
// 返回是否有新增产出物。
func DetectArtifacts(s *WorkflowState, dir string) bool {
	changed := false
	checks := []struct {
		stage Stage
		path  string
	}{
		{StageFlowAnalysis, ArtifactFlow},
		{StageModuleDesign, ArtifactModules},
		{StageQACases, ArtifactQACases},
	}
	for _, c := range checks {
		_, recorded := s.Artifacts[c.stage]
		if !recorded && fileExists(filepath.Join(dir, filepath.FromSlash(c.path))) {
			s.Artifacts[c.stage] = c.path
			RecordTimeline(s, c.stage, EventArtifactCreated, c.path)
			changed = true
		}
	}
	return changed
}

// NeedsReview 判断当前是否处于"审核点文档已产出但未审核"状态,门禁据此
// 注入审核提示(def 决定哪些阶段是审核点)。
func NeedsReview(def WorkflowDef, s *WorkflowState) bool {
	if !def.HasReview(s.Stage) {
		return false
	}
	if s.Stage == StageModuleDesign {
		_, hasModules := s.Artifacts[StageModuleDesign]
		return hasModules && s.Review == ReviewNone
	}
	return false
}

// --- 内部辅助 ---

type AdvanceError struct{ Reason string }

func (e *AdvanceError) Error() string { return e.Reason }

var errDone = &AdvanceError{Reason: "流程已处于终态 done,没有下一阶段"}

// normalizePath 归一化路径:去 ./ 前缀、转斜杠、清理。
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = filepath.ToSlash(filepath.Clean(strings.TrimPrefix(p, "./")))
	return strings.TrimPrefix(p, "/")
}

// underDocs 判断路径是否在 docs/ 目录下。
func underDocs(p string) bool {
	return p == "docs" || strings.HasPrefix(p, "docs/")
}

// isArtifact 判断路径是否命中指定产出物。
func isArtifact(p, artifact string) bool {
	return p == artifact || strings.HasPrefix(p, artifact+"/")
}

// fileExists 判断文件存在(且非目录)。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// runGo 在 dir 下执行 go <sub> <args...>,返回组合输出与错误。
// 超时兜底避免命令挂死(对齐 BashTool 的超时防御策略)。
func runGo(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	var sb strings.Builder
	cmd.Stdout = &sb
	cmd.Stderr = &sb
	err := cmd.Run()
	out := strings.TrimSpace(sb.String())
	if out == "" {
		out = "[no output]"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return out + "\n[timeout: go command exceeded " + checkTimeout.String() + "]", ctx.Err()
	}
	return out, err
}
