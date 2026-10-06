// simulator_test.go — 报告提取与校验回归测试。
// 重点覆盖:MarshalIndent 多行嵌套 JSON 曾被 LastIndex("{") 截错位置,
// 报 "invalid character ']' after top-level value"。

package simulator

import (
	"encoding/json"
	"strings"
	"testing"
)

// multiLineReport 模拟 agent 生成的 MarshalIndent 美化报告(与线上报错样本同构)。
const multiLineReport = `{
  "coin_conservation": {
    "error_pct": 0
  },
  "error_matches": 0,
  "matches": 2000,
  "probability_checks": [
    {
      "actual": 34.55,
      "expected": 45,
      "name": "landlord win rate",
      "pass": true,
      "tolerance_pct": 50
    },
    {
      "actual": 3.45,
      "expected": 15,
      "name": "rocket frequency",
      "pass": true,
      "tolerance_pct": 80
    }
  ]
}`

func TestExtractJSONReportMultilineIndent(t *testing.T) {
	got := extractJSONReport(multiLineReport)
	if got == "" {
		t.Fatal("未提取到 JSON 报告")
	}
	var r Report
	if err := json.Unmarshal([]byte(got), &r); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if r.Matches != 2000 || len(r.ProbabilityChecks) != 2 {
		t.Fatalf("解析结果异常: matches=%d checks=%d", r.Matches, len(r.ProbabilityChecks))
	}
}

func TestExtractJSONReportWithSurroundingLogs(t *testing.T) {
	// 前有日志 + 后有日志(旧 LastIndex 策略在尾部日志含 { 时也截错)
	out := "go: downloading deps\nroom-1 joined {player p0}\n" + multiLineReport + "\n2026-10-06 done, stats [{ok:true}]\n"
	got := extractJSONReport(out)
	if got == "" {
		t.Fatal("未提取到 JSON 报告")
	}
	var r Report
	if err := json.Unmarshal([]byte(got), &r); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if r.Matches != 2000 {
		t.Fatalf("matches=%d, want 2000", r.Matches)
	}
}

func TestExtractJSONReportBracesInStrings(t *testing.T) {
	// 字符串值含花括号与转义引号,配对扫描不能把它们算作结构括号
	out := `{"matches": 1500, "coin_conservation": {"error_pct": 0.1}, "probability_checks": [{"name": "rate {a} \"b\"", "expected": 1, "actual": 1, "tolerance_pct": 10, "pass": true}]}`
	got := extractJSONReport(out)
	if got == "" {
		t.Fatal("未提取到 JSON 报告")
	}
	var r Report
	if err := json.Unmarshal([]byte(got), &r); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if r.Matches != 1500 || r.ProbabilityChecks[0].Name != `rate {a} "b"` {
		t.Fatalf("解析结果异常: %+v", r)
	}
}

func TestExtractJSONReportNoJSON(t *testing.T) {
	if got := extractJSONReport("no json here, just logs\nroom closed"); got != "" {
		t.Fatalf("不应提取到报告, got %q", got)
	}
}

func TestExtractJSONReportPicksLargestValidBlock(t *testing.T) {
	// 日志里有一个小的合法 JSON 块,报告是最大的块;必须选报告
	out := `log {"code": 0}` + "\n" + multiLineReport
	got := extractJSONReport(out)
	if !strings.HasPrefix(got, "{\n  \"coin_conservation\"") {
		t.Fatalf("应提取最大的报告块, got %.60s", got)
	}
}
