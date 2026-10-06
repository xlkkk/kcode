// simulator_tools.go — 对局模拟工具。
//
// simulate_match 是 self_test 阶段的收尾验证:运行 agent 生成的
// simulation/main.go 对局模拟器,校验局数下限、金币流水守恒与概率分布,
// 达标后置位 WorkflowState.SimulationPassed(require_simulation 为 true
// 时 self_test → qa_cases 的门禁硬校验该标记)。
//
// 工具名与 workflow.ToolSimulateMatch 保持一致;注册发生在 Agent 工具
// 定格之后(见 internal/agent/agent.go),只对 Lead 可见。

package tools

import (
	"context"
	"fmt"

	"kcode/internal/simulator"
	"kcode/internal/workflow"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// SimulateMatchToolName 对局模拟工具名(workflow 包常量同值)。
const SimulateMatchToolName = "simulate_match"

// SimulateMatchTool 对局模拟:执行模拟程序 → 校验报告 → 写回状态机。
type SimulateMatchTool struct {
	state *workflow.WorkflowState
	store *workflow.Store
	dir   string
}

var _ tool.InvokableTool = &SimulateMatchTool{}

// NewSimulateMatchTool 创建模拟工具。dir 是项目 workDir(模拟程序在
// dir/simulation/ 下);state/store 用于写回模拟结论与时间线。
func NewSimulateMatchTool(state *workflow.WorkflowState, store *workflow.Store, dir string) tool.BaseTool {
	return &SimulateMatchTool{state: state, store: store, dir: dir}
}

func (t *SimulateMatchTool) Name() string { return SimulateMatchToolName }

func (t *SimulateMatchTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: SimulateMatchToolName,
		Desc: `Run the match simulator to validate game probability and coin conservation. ` +
			`Before calling this, you must generate a standalone Go program at simulation/main.go in the project that plays N bot-vs-bot matches (N >= 1000) and prints a JSON report to stdout like: ` +
			`{"matches":10000,"coin_conservation":{"error_pct":0.02},"probability_checks":[{"name":"bomb probability","expected":2.0,"actual":1.98,"tolerance_pct":10,"pass":true}]}. ` +
			`The tool runs "go run ." in simulation/, verifies min matches, coin conservation error <= 0.5%, and that every probability_check passes. ` +
			`On pass it marks the workflow simulation as done so you can advance to qa_cases; on failure it returns the problems for you to fix and re-run.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *SimulateMatchTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if t.state.Stage != workflow.StageSelfTest {
		return "", fmt.Errorf("simulate_match 只能在 self_test 阶段调用(当前 %s)", t.state.Stage)
	}

	report, err := simulator.RunSimulation(ctx, t.dir)
	if err != nil {
		return "", err
	}
	result := simulator.VerifyReport(report, simulator.VerifyOptions{
		MinMatches:     simulator.DefaultMinMatches,
		MinProbability: 1,
	})
	summary := simulator.Summarize(report, result)

	t.state.SimulationPassed = result.Pass
	t.state.SimulationReport = summary
	event := workflow.EventSimulationFailed
	detail := summary
	if result.Pass {
		event = workflow.EventSimulationPassed
		detail = fmt.Sprintf("%d 局模拟达标,金币守恒误差 %.3f%%", report.Matches, report.CoinConservation.ErrorPct)
	}
	workflow.RecordTimeline(t.state, t.state.Stage, event, detail)
	if t.store != nil {
		_ = t.store.Save(t.state)
	}

	if result.Pass {
		return "对局模拟达标。\n\n" + summary + "\n\n现在可以调用 advance_stage 进入 qa_cases 阶段。", nil
	}
	return "对局模拟未达标。\n\n" + summary + "\n\n请修复问题后重新调用 simulate_match。", nil
}
