package main

import (
	"chessmaster/cert"
	"chessmaster/engine"
	"chessmaster/logger"
	"chessmaster/proxy"
	"chessmaster/sysproxy"
	"chessmaster/webui"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 中国象棋开局 FEN（红方先走）
const startFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

func main() {
	proxyAddr := flag.String("proxy", ":8899", "代理监听地址（例如 :8899）")
	logDir := flag.String("logdir", "logs", "日志输出目录")
	maxMB := flag.Int64("maxmb", 20, "单个日志文件上限（MB）")
	engineExe := flag.String("engineexe", "pikafish/pikafish-avx2.exe", "Pikafish 引擎可执行文件路径")
	engineTest := flag.Bool("enginetest", false, "仅测试引擎调用：开局算一步后退出，不启动代理")
	enableAdvisor := flag.Bool("advisor", true, "是否启用引擎推荐")
	mySeat := flag.Int("myseat", -1, "我方席位（0=黑 1=红，-1=进入对局后自动识别）")
	movetimeMs := flag.Int("movetime", 3000, "引擎思考时长（毫秒）")
	threads := flag.Int("threads", 4, "引擎线程数")
	webAddr := flag.String("web", ":8900", "Web 可视化监听地址（空字符串禁用）")
	flag.Parse()

	if *engineTest {
		runEngineTest(*engineExe)
		return
	}

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║       ChessMaster 微信小程序抓包工具           ║")
	fmt.Println("╚══════════════════════════════════════════════╝")

	// 1. 加载或生成 CA 证书
	ca, err := cert.LoadOrCreate()
	if err != nil {
		log.Fatalf("CA 初始化失败: %v", err)
	}
	if _, err := os.Stat(cert.CACertFile); err == nil {
		fmt.Printf("✓ CA 证书: %s\n", cert.CACertFile)
	}

	// 2. 初始化文件日志
	l := logger.New(*logDir, *maxMB)
	defer l.Close()
	fmt.Printf("✓ 日志目录: %s\\\n", *logDir)

	// 3. 启动代理服务器
	p := proxy.New(ca, l)

	// 3.5 可选：启动引擎推荐器
	var advisor *engine.Advisor
	if *enableAdvisor {
		eng, err := engine.Start(engine.Options{
			ExePath: *engineExe,
			Threads: *threads,
			Hash:    256,
		})
		if err != nil {
			log.Printf("⚠ 引擎启动失败，推荐功能关闭: %v", err)
		} else {
			advisor = engine.NewAdvisor(eng, *mySeat, *movetimeMs)
			p.SetAdvisor(advisor)
			defer advisor.Stop()
			seatDesc := "自动识别"
			if *mySeat >= 0 {
				seatDesc = engine.SeatName(*mySeat)
			}
			fmt.Printf("✓ 引擎推荐: 已启用 (%s、思考%dms、%d线程)\n",
				seatDesc, *movetimeMs, *threads)
		}
	}

	proxyServer := &http.Server{
		Addr:    *proxyAddr,
		Handler: p,
	}
	go func() {
		fmt.Printf("✓ 代理监听: 127.0.0.1%s\n", *proxyAddr)
		if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("代理服务器启动失败: %v", err)
		}
	}()

	// 3.6 Web 可视化服务器（实时棋盘 + 推荐点位）
	var webServer *webui.Server
	var browserCmd *exec.Cmd
	if advisor != nil && *webAddr != "" {
		webServer = webui.New(advisor, *webAddr)
		if err := webServer.Start(); err != nil {
			log.Printf("⚠ Web 可视化启动失败: %v", err)
			webServer = nil
		} else {
			advisor.SetWatcher(webServer.Broadcast)
			host := *webAddr
			if strings.HasPrefix(host, ":") {
				host = "127.0.0.1" + host
			}
			url := "http://" + host
			fmt.Printf("✓ Web 棋盘可视化: %s\n", url)
			// 启动独立应用窗口（退出时一并关闭）
			browserCmd = webui.LaunchBrowser(url)
		}
	}

	// 4. 自动开启 Windows 系统代理，退出时还原
	sysproxyAddr := buildSysProxyAddr(*proxyAddr)
	origState, _ := sysproxy.Get()
	if err := sysproxy.Set(sysproxyAddr); err != nil {
		log.Printf("⚠ 系统代理开启失败（请手动设置）: %v", err)
	} else {
		fmt.Printf("✓ Windows 系统代理已自动开启 → %s\n", sysproxyAddr)
	}
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			if err := sysproxy.Restore(origState); err != nil {
				log.Printf("⚠ 还原系统代理失败: %v", err)
			} else {
				fmt.Println("✓ Windows 系统代理已还原")
			}
		})
	}
	defer restore()

	// 注册 Windows 控制台事件处理器：点 X 关闭、注销、关机时也能
	// 同步还原系统代理并关闭浏览器（避免 Windows 5 秒强杀造成代理残留）
	setupConsoleCleanup(func() {
		restore()
		webui.CloseBrowser(browserCmd)
	})

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━ 配置步骤 ━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("1. 设置 Windows 代理: 地址 127.0.0.1  端口 %s\n", (*proxyAddr)[1:])
	fmt.Printf("2. 日志实时写入 %s\\ 目录:\n", *logDir)
	fmt.Println("     http_YYYY-MM-DD.jsonl  ← HTTP/HTTPS 请求")
	fmt.Println("     ws_YYYY-MM-DD.jsonl    ← WebSocket 帧")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("按 Ctrl+C 停止，日志自动保存")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("\n正在关闭...")
	restore()                      // 提前还原系统代理，避免后续关闭超时出现窗口卡顿
	webui.CloseBrowser(browserCmd) // 关闭 Web 页面窗口
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if webServer != nil {
		_ = webServer.Shutdown(shutdownCtx)
	}
	_ = proxyServer.Shutdown(shutdownCtx)
	fmt.Println("日志已保存")
}

// buildSysProxyAddr 将 -proxy 参数如 ":8899" / "127.0.0.1:8899" / "0.0.0.0:8899"
// 转为适合系统代理的 host:port，0.0.0.0 会被替换为 127.0.0.1
func buildSysProxyAddr(addr string) string {
	host, port := "127.0.0.1", strings.TrimPrefix(addr, ":")
	if i := strings.LastIndex(addr, ":"); i > 0 {
		h := addr[:i]
		if h != "" && h != "0.0.0.0" {
			host = h
		}
		port = addr[i+1:]
	}
	return host + ":" + port
}

// runEngineTest 启动 Pikafish 用开局局面算一步，验证引擎集成是否正常
func runEngineTest(exePath string) {
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║         Pikafish 引擎调用测试                  ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Printf("引擎路径: %s\n\n", exePath)

	start := time.Now()
	eng, err := engine.Start(engine.Options{
		ExePath: exePath,
		Threads: 2,
		Hash:    128,
		Debug:   true,
	})
	if err != nil {
		log.Fatalf("引擎启动失败: %v", err)
	}
	defer eng.Stop()
	fmt.Printf("\n✓ 引擎握手完成 (%v)\n\n", time.Since(start).Round(time.Millisecond))

	fmt.Println("使用开局 FEN 让引擎思考 3 秒…")
	fmt.Printf("FEN: %s\n\n", startFEN)

	t0 := time.Now()
	move, err := eng.BestMove(startFEN, 3000)
	if err != nil {
		log.Fatalf("获取 bestmove 失败: %v", err)
	}
	fmt.Printf("\n✓ 推荐着法: %s   (耗时 %v)\n", move, time.Since(t0).Round(time.Millisecond))
}
