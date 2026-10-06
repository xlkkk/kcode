// simulator.go — 棋牌对局模拟器执行与报告校验。
//
// 设计目标:把 self_test 阶段的「模拟对局」从口头约束升级为硬门禁。
// 约定一个与游戏代码解耦的 JSON 报告协议:
//
//	agent 在 self_test 阶段生成 simulation/main.go(可独立运行的程序),
//	把 N 局机器人对局的统计结果以 JSON 输出到 stdout:
//
//	{
//	  "matches": 10000,
//	  "coin_conservation": {"error_pct": 0.02},
//	  "probability_checks": [
//	    {"name": "王炸出现概率", "expected": 2.0, "actual": 1.98, "tolerance_pct": 10, "pass": true}
//	  ]
//	}
//
// RunSimulation 定位并执行模拟程序;VerifyReport 是纯函数校验(局数下限、
// 金币流水守恒误差、概率偏差容忍度),供 simulate_match 工具与单测复用。
//
// 架构约定:本包只提供纯函数与执行逻辑,由工具层编排调用(与
// permissions/hooks 的「逻辑包纯函数 + Middleware/工具编排」模式一致)。

package simulator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 默认校验阈值(可在 VerifyOptions 中覆盖)。
const (
	// DefaultMinMatches 最少模拟局数,低于此值统计无意义。
	DefaultMinMatches = 1000
	// DefaultMaxCoinErrorPct 金币流水守恒最大允许误差(百分比)。
	DefaultMaxCoinErrorPct = 0.5
	// DefaultCheckTimeout 模拟程序执行超时(万局机器人对局可能较慢)。
	DefaultCheckTimeout = 10 * time.Minute
)

// Report 是模拟程序 stdout 输出的 JSON 报告协议(见文件头注释)。
type Report struct {
	Matches int `json:"matches"`

	// CoinConservation 金币流水守恒:整场模拟的 [总产出 - 总回收] 相对误差。
	CoinConservation struct {
		ErrorPct float64 `json:"error_pct"`
	} `json:"coin_conservation"`

	// ProbabilityChecks 概率验证项:策划案预期概率 vs 模拟实测概率。
	// pass 由模拟程序按 tolerance_pct 自行判定;VerifyReport 会复核。
	ProbabilityChecks []ProbabilityCheck `json:"probability_checks"`
}

// ProbabilityCheck 单条概率验证:expected/actual 为百分比,tolerance_pct
// 为允许的相对偏差(百分比)。
type ProbabilityCheck struct {
	Name         string  `json:"name"`
	Expected     float64 `json:"expected"`
	Actual       float64 `json:"actual"`
	TolerancePct float64 `json:"tolerance_pct"`
	Pass         bool    `json:"pass"`
}

// VerifyOptions 校验阈值配置(零值字段使用默认值)。
type VerifyOptions struct {
	MinMatches      int
	MaxCoinErrorPct float64
	MinProbability  int // 最少需要几条概率验证项(0 = 不强制)
}

// VerifyResult 校验结论:Pass 是否达标,Problems 列出未达标项。
type VerifyResult struct {
	Pass     bool
	Problems []string
}

// VerifyReport 校验模拟报告(纯函数,供工具与单测复用)。
// 三项硬性标准:局数下限、金币流水守恒、概率验证项全部通过。
func VerifyReport(r *Report, opts VerifyOptions) VerifyResult {
	minMatches := opts.MinMatches
	if minMatches == 0 {
		minMatches = DefaultMinMatches
	}
	maxCoinErr := opts.MaxCoinErrorPct
	if maxCoinErr == 0 {
		maxCoinErr = DefaultMaxCoinErrorPct
	}

	res := VerifyResult{Pass: true}

	if r.Matches < minMatches {
		res.Pass = false
		res.Problems = append(res.Problems, fmt.Sprintf(
			"模拟局数 %d 低于下限 %d,统计不可信", r.Matches, minMatches))
	}
	if r.CoinConservation.ErrorPct > maxCoinErr {
		res.Pass = false
		res.Problems = append(res.Problems, fmt.Sprintf(
			"金币流水守恒误差 %.3f%% 超过上限 %.2f%%,存在金币凭空产生/消失", r.CoinConservation.ErrorPct, maxCoinErr))
	}
	if len(r.ProbabilityChecks) < opts.MinProbability {
		res.Pass = false
		res.Problems = append(res.Problems, fmt.Sprintf(
			"概率验证项 %d 条少于要求 %d 条,请对照策划案补充关键概率检查", len(r.ProbabilityChecks), opts.MinProbability))
	}
	for _, pc := range r.ProbabilityChecks {
		if !pc.Pass {
			res.Pass = false
			res.Problems = append(res.Problems, fmt.Sprintf(
				"概率检查 %q 未通过:期望 %.2f%%,实测 %.2f%%(容忍 ±%.2f%%)",
				pc.Name, pc.Expected, pc.Actual, pc.TolerancePct))
		}
	}
	return res
}

// SimulationDir 是约定的模拟程序目录(备选 simulate/ 兼容旧命名)。
var simDirs = []string{"simulation", "simulate"}

// FindSimDir 定位模拟程序目录:存在 main.go 的 simulation/ 或 simulate/。
// 都不存在时返回 "" 与提示信息。
func FindSimDir(dir string) (string, string) {
	for _, sub := range simDirs {
		candidate := filepath.Join(dir, sub)
		if info, err := os.Stat(filepath.Join(candidate, "main.go")); err == nil && !info.IsDir() {
			return candidate, ""
		}
	}
	return "", "未找到模拟程序:请在项目下生成 simulation/main.go(独立可运行程序,向 stdout 输出 JSON 报告,协议见 simulate_match 工具说明)"
}

// RunSimulation 在项目 dir 下定位并执行模拟程序,解析 stdout 的 JSON 报告。
// 超时兜底避免机器人对局挂死(对齐 gate.runGo 的超时防御策略)。
func RunSimulation(ctx context.Context, dir string) (*Report, error) {
	simDir, hint := FindSimDir(dir)
	if simDir == "" {
		return nil, fmt.Errorf("%s", hint)
	}

	runCtx, cancel := context.WithTimeout(ctx, DefaultCheckTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "go", "run", ".")
	cmd.Dir = simDir
	var sb strings.Builder
	cmd.Stdout = &sb
	cmd.Stderr = &sb
	if err := cmd.Run(); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("模拟程序执行超时(%s):\n%s", DefaultCheckTimeout, sb.String())
		}
		return nil, fmt.Errorf("模拟程序执行失败: %w\n%s", err, sb.String())
	}

	// stdout 可能混有 go run 之外的日志;用括号深度配对扫描提取完整 JSON
	// 块(兼容多行缩进 JSON 与前/后日志两种场景)。旧实现 LastIndex("{")
	// 截取在 MarshalIndent 多行嵌套报告上会截错位置,报
	// "invalid character ']' after top-level value"。
	out := sb.String()
	reportJSON := extractJSONReport(out)
	if reportJSON == "" {
		return nil, fmt.Errorf("模拟程序未输出 JSON 报告:\n%s", out)
	}
	var report Report
	if err := json.Unmarshal([]byte(reportJSON), &report); err != nil {
		return nil, fmt.Errorf("解析模拟报告 JSON 失败: %w\n原始输出:\n%s", err, out)
	}
	return &report, nil
}

// extractJSONReport 从可能混有日志的输出中提取 JSON 报告。
// 括号深度配对扫描(正确处理字符串内的花括号与转义),找出所有完整的
// 顶层 {...} 块,优先返回最大且合法的一个(报告通常远大于日志片段)。
func extractJSONReport(out string) string {
	type block struct{ start, end int }
	var blocks []block
	inStr, esc, depth, start := false, false, 0, -1
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					blocks = append(blocks, block{start, i + 1})
				}
			}
		}
	}
	best := -1
	for i := range blocks {
		b := blocks[i]
		if !json.Valid([]byte(out[b.start:b.end])) {
			continue
		}
		if best < 0 || b.end-b.start > blocks[best].end-blocks[best].start {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return out[blocks[best].start:blocks[best].end]
}

// Summarize 生成报告的可读摘要(写回 state.SimulationReport 与工具回显)。
func Summarize(r *Report, v VerifyResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "对局模拟: %d 局\n", r.Matches)
	fmt.Fprintf(&sb, "金币流水守恒误差: %.3f%%\n", r.CoinConservation.ErrorPct)
	if len(r.ProbabilityChecks) > 0 {
		sb.WriteString("概率验证:\n")
		for _, pc := range r.ProbabilityChecks {
			mark := "通过"
			if !pc.Pass {
				mark = "未通过"
			}
			fmt.Fprintf(&sb, "  - %s: 期望 %.2f%% / 实测 %.2f%% (%s)\n", pc.Name, pc.Expected, pc.Actual, mark)
		}
	}
	if v.Pass {
		sb.WriteString("结论: 达标")
	} else {
		sb.WriteString("结论: 未达标\n")
		for _, p := range v.Problems {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}
	return sb.String()
}
