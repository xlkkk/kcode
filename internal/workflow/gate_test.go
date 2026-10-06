package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNextStageChain(t *testing.T) {
	want := []Stage{StageFlowAnalysis, StageModuleDesign, StageCodeGen, StageSelfTest, StageQACases, StageDone, ""}
	cur := StageInit
	for _, next := range want {
		if got := NextStage(cur); got != next {
			t.Fatalf("NextStage(%s) = %s, want %s", cur, got, next)
		}
		if next == "" {
			break
		}
		cur = next
	}
}

func TestCanUseToolMatrix(t *testing.T) {
	cases := []struct {
		name    string
		stage   Stage
		tool    string
		path    string
		allowed bool
	}{
		// init:一切写被禁
		{"init 写代码", StageInit, ToolWriteFile, "main.go", false},
		{"init 写文档", StageInit, ToolWriteFile, "docs/flow.md", false},
		// flow_analysis:仅 docs/
		{"flow 写 flow.md", StageFlowAnalysis, ToolWriteFile, "docs/flow.md", true},
		{"flow 写 modules.md", StageFlowAnalysis, ToolWriteFile, "docs/modules.md", false},
		{"flow 写代码", StageFlowAnalysis, ToolEditFile, "main.go", false},
		// module_design:仅 docs/(flow.md 锁定)
		{"module 写 modules.md", StageModuleDesign, ToolWriteFile, "docs/modules.md", true},
		{"module 改 flow.md", StageModuleDesign, ToolEditFile, "docs/flow.md", false},
		{"module 写代码", StageModuleDesign, ToolWriteFile, "internal/game.go", false},
		// code_gen / self_test:docs/ 锁定,其余放行
		{"codegen 写代码", StageCodeGen, ToolWriteFile, "internal/game.go", true},
		{"codegen 改 flow.md", StageCodeGen, ToolEditFile, "docs/flow.md", false},
		{"selftest 写测试", StageSelfTest, ToolWriteFile, "internal/game_test.go", true},
		{"selftest 写 docs", StageSelfTest, ToolWriteFile, "docs/anything.md", false},
		// qa_cases:仅 qa_cases.md
		{"qa 写 qa_cases.md", StageQACases, ToolWriteFile, "docs/qa_cases.md", true},
		{"qa 写 flow.md", StageQACases, ToolWriteFile, "docs/flow.md", false},
		{"qa 写代码", StageQACases, ToolWriteFile, "main.go", false},
		// done:全放行
		{"done 写代码", StageDone, ToolWriteFile, "main.go", true},
		// 飞书工具:read 任意阶段,write 仅 qa_cases/done
		{"read_feishu init 可用", StageInit, ToolReadFeishuDoc, "", true},
		{"read_feishu codegen 可用", StageCodeGen, ToolReadFeishuDoc, "", true},
		{"write_feishu init 禁用", StageInit, ToolWriteFeishuDoc, "", false},
		{"write_feishu codegen 禁用", StageCodeGen, ToolWriteFeishuDoc, "", false},
		{"write_feishu qa 可用", StageQACases, ToolWriteFeishuDoc, "", true},
		{"write_feishu done 可用", StageDone, ToolWriteFeishuDoc, "", true},
		// 非写类工具不受门禁
		{"只读放行", StageInit, "read_file", "docs/flow.md", true},
		{"bash 不拦截", StageInit, "bash", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, _ := CanUseTool(c.stage, c.tool, c.path)
			if ok != c.allowed {
				t.Fatalf("CanUseTool(%s, %s, %s) = %v, want %v", c.stage, c.tool, c.path, ok, c.allowed)
			}
		})
	}
}

func TestCanAdvanceReviewGate(t *testing.T) {
	def := DefaultDef()

	// flow → module:需要 flow.md 产出
	s := NewState()
	s.Stage = StageFlowAnalysis
	if ok, _ := CanAdvance(def, s, StageModuleDesign); ok {
		t.Fatal("缺少 flow.md 时不应允许推进到 module_design")
	}
	s.Artifacts[StageFlowAnalysis] = ArtifactFlow
	if ok, _ := CanAdvance(def, s, StageModuleDesign); !ok {
		t.Fatal("flow.md 已产出,应允许推进到 module_design")
	}

	// module → code:需要 modules.md + 审核通过
	s.Stage = StageModuleDesign
	if ok, _ := CanAdvance(def, s, StageCodeGen); ok {
		t.Fatal("缺少 modules.md 时不应允许推进到 code_gen")
	}
	s.Artifacts[StageModuleDesign] = ArtifactModules
	if ok, _ := CanAdvance(def, s, StageCodeGen); ok {
		t.Fatal("审核未通过时不应允许推进到 code_gen")
	}
	s.Review = ReviewRejected
	if ok, _ := CanAdvance(def, s, StageCodeGen); ok {
		t.Fatal("审核被驳回时不应允许推进到 code_gen")
	}
	s.Review = ReviewApproved
	if ok, _ := CanAdvance(def, s, StageCodeGen); !ok {
		t.Fatal("审核通过后应允许推进到 code_gen")
	}

	// 不允许跳阶段
	s2 := NewState()
	if ok, _ := CanAdvance(def, s2, StageCodeGen); ok {
		t.Fatal("不允许从 init 直接跳到 code_gen")
	}
}

func TestCanAdvanceRequirePlanGate(t *testing.T) {
	def := DefaultDef()
	def.RequirePlan = true

	s := NewState()
	if ok, reason := CanAdvance(def, s, StageFlowAnalysis); ok {
		t.Fatalf("require_plan 开启且未读策划案时不应允许推进, reason=%q", reason)
	}
	s.PlanLoaded = true
	if ok, _ := CanAdvance(def, s, StageFlowAnalysis); !ok {
		t.Fatal("已读取策划案,应允许推进到 flow_analysis")
	}

	// require_plan 关闭时不拦截
	def.RequirePlan = false
	s.PlanLoaded = false
	if ok, _ := CanAdvance(def, s, StageFlowAnalysis); !ok {
		t.Fatal("require_plan 关闭时不应拦截 init → flow_analysis")
	}
}

func TestCanAdvanceReviewDisabled(t *testing.T) {
	// 配置去掉 module_design 审核点后,code_gen 推进不再要求审核。
	def := DefaultDef()
	def.ReviewStages = nil

	s := NewState()
	s.Stage = StageModuleDesign
	s.Artifacts[StageModuleDesign] = ArtifactModules
	if ok, _ := CanAdvance(def, s, StageCodeGen); !ok {
		t.Fatal("未配置审核点时不应要求审核通过")
	}
}

func TestWorkflowDefValidate(t *testing.T) {
	def := DefaultDef()
	if err := def.Validate(); err != nil {
		t.Fatalf("默认定义应合法: %v", err)
	}
	def.ReviewStages = []Stage{StageFlowAnalysis}
	if err := def.Validate(); err == nil {
		t.Fatal("非 module_design 审核点应校验失败")
	}
	if !def.HasReview(StageFlowAnalysis) {
		t.Fatal("HasReview 应命中配置的审核点")
	}
	if DefaultDef().HasReview(StageCodeGen) {
		t.Fatal("默认定义不应在 code_gen 设审核点")
	}
}

func TestStageNodes(t *testing.T) {
	s := NewState()
	s.Stage = StageCodeGen
	nodes := StageNodes(s)
	if len(nodes) != 7 {
		t.Fatalf("StageNodes 数量 = %d, want 7", len(nodes))
	}
	for _, n := range nodes {
		if n.Name == StageCodeGen && !n.Current {
			t.Fatal("当前阶段应标记 Current=true")
		}
		if n.Name == StageFlowAnalysis && !n.Done {
			t.Fatal("已过阶段应标记 Done=true")
		}
		if n.Name == StageSelfTest && (n.Current || n.Done) {
			t.Fatal("未到阶段不应标记 Current/Done")
		}
	}
}

func TestAdvancePersistsReviewReset(t *testing.T) {
	s := NewState()
	s.Stage = StageModuleDesign
	s.Artifacts[StageModuleDesign] = ArtifactModules
	s.Review = ReviewApproved
	s.ReviewFeedback = ""

	next, err := Advance(DefaultDef(), s, t.TempDir())
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if next != StageCodeGen {
		t.Fatalf("Advance = %s, want %s", next, StageCodeGen)
	}
	if s.Review != ReviewNone || s.ReviewFeedback != "" {
		t.Fatal("跨过审核点后审核状态应清空")
	}
}

// TestVerifyTransitionBuildAndTest 用最小 Go 模块验证 go build / go test 校验。
func TestVerifyTransitionBuildAndTest(t *testing.T) {
	dir := t.TempDir()
	gomod := "module game\n\ngo 1.21\n"
	mainGo := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "game"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "game", "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatal(err)
	}

	// code_gen → self_test:go build 通过
	s := NewState()
	s.Stage = StageCodeGen
	if ok, reason := VerifyTransition(DefaultDef(), s, dir, string(StageSelfTest)); !ok {
		t.Fatalf("go build 校验失败: %s", reason)
	}

	// self_test → qa_cases:go test 通过(无测试文件也算绿),模拟达标
	// (RequireSimulation 默认开启,需置位 SimulationPassed)。
	s.Stage = StageSelfTest
	s.SimulationPassed = true
	if ok, reason := VerifyTransition(DefaultDef(), s, dir, string(StageQACases)); !ok {
		t.Fatalf("go test 校验失败: %s", reason)
	}

	// 配置关闭 require_build/require_test 后跳过实跑校验
	def := DefaultDef()
	def.RequireBuild = false
	def.RequireTest = false
	s.Stage = StageCodeGen
	if ok, reason := VerifyTransition(def, s, dir, string(StageSelfTest)); !ok {
		t.Fatalf("关闭 require_build 后不应实跑 go build: %s", reason)
	}
	s.Stage = StageSelfTest
	if ok, reason := VerifyTransition(def, s, dir, string(StageQACases)); !ok {
		t.Fatalf("关闭 require_test 后不应实跑 go test: %s", reason)
	}
}

func TestDetectArtifacts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "flow.md"), []byte("# flow"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewState()
	if !DetectArtifacts(s, dir) {
		t.Fatal("应检测到新产出物")
	}
	if s.Artifacts[StageFlowAnalysis] != ArtifactFlow {
		t.Fatalf("Artifacts[flow] = %q, want %q", s.Artifacts[StageFlowAnalysis], ArtifactFlow)
	}
	if DetectArtifacts(s, dir) {
		t.Fatal("无新增产出物时不应报告变化")
	}
	if !NeedsReview(DefaultDef(), &WorkflowState{Stage: StageModuleDesign, Review: ReviewNone, Artifacts: map[Stage]string{StageModuleDesign: ArtifactModules}}) {
		t.Fatal("modules.md 产出且未审核时应 NeedsReview")
	}
	if NeedsReview(DefaultDef(), &WorkflowState{Stage: StageModuleDesign, Review: ReviewPending, Artifacts: map[Stage]string{StageModuleDesign: ArtifactModules}}) {
		t.Fatal("已提交审核时不应 NeedsReview")
	}
	// 审核点被配置移除后不再 NeedsReview
	noReview := DefaultDef()
	noReview.ReviewStages = nil
	if NeedsReview(noReview, &WorkflowState{Stage: StageModuleDesign, Review: ReviewNone, Artifacts: map[Stage]string{StageModuleDesign: ArtifactModules}}) {
		t.Fatal("未配置审核点时不应 NeedsReview")
	}
}
