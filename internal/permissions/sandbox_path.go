package permissions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 路径沙箱 — 文件操作限制在项目目录内
// 备注 internal/permissions/permissions.go PathSandbox

// PathSandbox 路径沙箱
type PathSandbox struct {
	allowedRoots []string // 允许访问的根目录
	denyWrite    []string // 始终只读的受保护路径
}

// NewPathSandbox 创建路径沙箱
//
// projectRoot: 项目根目录（允许读写）
// extraAllowed: 额外允许的目录
func NewPathSandbox(projectRoot string, extraAllowed ...string) *PathSandbox {
	root, _ := filepath.Abs(projectRoot)
	allowed := []string{root, os.TempDir()}
	for _, p := range extraAllowed {
		abs, _ := filepath.Abs(p)
		allowed = append(allowed, abs)
	}

	// 受保护路径：防止 Agent 篡改配置和 Skill 定义
	denyWrite := []string{
		filepath.Join(root, ".kcode", "config.yaml"),
		filepath.Join(root, ".kcode", "permissions.local.yaml"),
		filepath.Join(root, ".kcode", "skills"),
	}

	return &PathSandbox{allowedRoots: allowed, denyWrite: denyWrite}
}

// CheckDenyWrite 检查受保护路径（写入拒绝）
//
// 受保护路径存放权限配置与 Skill 定义，
// 任何权限模式下都不允许写入。
func (s *PathSandbox) CheckDenyWrite(path string) (bool, string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Sprintf("cannot resolve path: %s", path)
	}
	for _, deny := range s.denyWrite {
		if abs == deny || strings.HasPrefix(abs, deny+string(filepath.Separator)) {
			return false, fmt.Sprintf("protected path: %s", path)
		}
	}
	return true, ""
}

// Check 检查路径是否在沙箱范围内
func (s *PathSandbox) Check(path string) (bool, string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Sprintf("cannot resolve path: %s", path)
	}

	// 先检查受保护路径
	if ok, reason := s.CheckDenyWrite(path); !ok {
		return false, reason
	}

	// 检查是否在允许的根目录内
	for _, root := range s.allowedRoots {
		if strings.HasPrefix(abs, root) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("path %s outside sandbox", path)
}

// GetDenyWrite 返回受保护路径列表
func (s *PathSandbox) GetDenyWrite() []string {
	return s.denyWrite
}

// GetAllowedRoots 返回允许的根目录列表
func (s *PathSandbox) GetAllowedRoots() []string {
	return s.allowedRoots
}
