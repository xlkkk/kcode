package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"kcode/internal/config"
	"kcode/internal/web"
)

func main() {
	webMode := flag.Bool("web", false, "start the Web UI server (WebSocket + static SPA)")
	port := flag.Int("port", 8080, "port for --web mode")
	staticDir := flag.String("static", "", "directory holding the built web UI (optional)")
	workDir := flag.String("dir", "", "project working directory (default: cwd)")
	configPath := flag.String("config", "", "path to config file (default: auto-detect ~/.kcode/config.yaml, ./.kcode/config.yaml, ./.kcode/config.local.yaml)")
	flag.Parse()

	// 未显式指定 --static 时自动探测默认静态目录(bin/static、web/static)。
	if *staticDir == "" {
		for _, d := range []string{"bin/static", "web/static", "static"} {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				*staticDir = d
				break
			}
		}
	}

	if !*webMode {
		// CLI / TUI 模式保持原状（尚未实现），只做提示。
		fmt.Println("kcode — AI Coding Agent powered by Eino ADK")
		fmt.Println("TODO: implement")
		fmt.Println("hint: run with --web to start the web server")
		os.Exit(0)
	}

	wd := *workDir
	if wd == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "kcode: %v\n", err)
			os.Exit(1)
		}
		wd = cwd
	}

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kcode: config error: %v\n", err)
		os.Exit(1)
	}

	srv := web.NewServer(cfg, wd, *staticDir, fmt.Sprintf(":%d", *port))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("kcode web server listening on http://localhost:%d (workdir: %s", *port, wd)
	if *staticDir != "" {
		fmt.Printf(", static: %s", *staticDir)
	}
	fmt.Println(")")
	if err := srv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "kcode: server error: %v\n", err)
		os.Exit(1)
	}
}
