// server.go — HTTP + WebSocket 服务入口。
//
// 对应移植计划 Phase 7.1：
//   - /ws           每条连接升级为一个 Agent Session（见 session.go）；
//   - /api/health   存活探测；
//   - /api/info     前端启动信息（模型 / 权限模式 / workDir）；
//   - 静态文件       前端 SPA 打包产物，带 index.html fallback。

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kcode/internal/config"
)

// Server 持有启动期就能确定的共享状态；每连接的运行态在 Session 里。
type Server struct {
	cfg       *config.AppConfig
	workDir   string
	staticDir string
	addr      string
}

// NewServer 创建 Web 服务。staticDir 为空时只服务 /ws 与 /api/*。
func NewServer(cfg *config.AppConfig, workDir, staticDir, addr string) *Server {
	return &Server{
		cfg:       cfg,
		workDir:   workDir,
		staticDir: staticDir,
		addr:      addr,
	}
}

// Start 阻塞服务直到 ctx 取消，随后优雅关闭（10s 收尾）。
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/info", s.handleInfo)
	if s.staticDir != "" {
		mux.Handle("/", s.handleStatic())
	}

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// --- handlers ---

// handleWS 升级连接并交给 Session 跑完整生命周期。
// 模型用第一个 provider；多 provider 切换暂未纳入本期计划。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := newWSConn(w, r)
	if err != nil {
		// Upgrade 失败时 gorilla 已写好 HTTP 错误，这里只需返回。
		return
	}
	provider := &s.cfg.Providers[0]
	NewSession(provider, s.workDir, conn).Serve()
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInfo 前端启动时拉取的基础信息。
func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	p := s.cfg.Providers[0]
	writeJSON(w, http.StatusOK, map[string]any{
		"work_dir":        s.workDir,
		"provider_name":   p.Name,
		"model":           p.Model,
		"protocol":        p.Protocol,
		"permission_mode": s.cfg.PermissionMode,
	})
}

// handleStatic 服务 SPA 静态产物：命中文件直出，未命中回退 index.html
// （前端路由由 SPA 自己处理）；staticDir 不存在时给出可操作提示。
func (s *Server) handleStatic() http.Handler {
	fs := http.FileServer(http.Dir(s.staticDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(s.staticDir); err != nil {
			http.Error(w, fmt.Sprintf("static dir %q not found; build the web UI or omit it", s.staticDir), http.StatusNotFound)
			return
		}
		path := filepath.Join(s.staticDir, filepath.Clean(strings.TrimPrefix(r.URL.Path, "/")))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		// API 路径不做 SPA fallback，避免把 404 伪装成页面。
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.staticDir, "index.html"))
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
