package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/app"
	"cursor-inner/internal/autostart"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/console"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/fsutil"
	"cursor-inner/internal/logx"
	"cursor-inner/internal/mitm"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/web"
)

var version = "dev"

type consoleEvents struct{ c *console.Console }

func (e consoleEvents) Write(p []byte) (int, error) {
	e.c.Event(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func main() {
	opt := parseArgs(os.Args[1:])
	if opt.watch > 0 {
		dir := opt.dataDir
		if dir == "" {
			dir = config.DefaultDir()
		}
		takeover.RunWatch(opt.watch, dir)
		return
	}

	dir := opt.dataDir
	if dir == "" {
		if opt.debug {
			var err error
			dir, err = os.MkdirTemp("", "cursor-inner-debug-")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		} else {
			dir = config.DefaultDir()
		}
	}
	noTakeover := opt.noTakeover
	release, already, err := acquireSingleton(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if already {
		url := ""
		if raw, err := os.ReadFile(filepath.Join(dir, "listen.url")); err == nil {
			url = strings.TrimSpace(string(raw))
			fmt.Println(url)
		}
		notifyAlreadyRunning(url)
		os.Exit(0)
	}
	if needsClassicConsole() {
		release()
		if startClassicConsole() == nil {
			return
		}
		if release, already, err = acquireSingleton(dir); err != nil || already {
			os.Exit(1)
		}
	}
	defer release()
	brandConsoleWindow()

	var logFile io.Writer
	if err := os.MkdirAll(dir, 0o755); err == nil {
		logPath := filepath.Join(dir, "inner.log")
		_ = logx.Rotate(logPath)
		if f, err := os.Create(logPath); err == nil {
			defer f.Close()
			logFile = f
		}
	}
	term := console.New(os.Stdout, logFile)
	defer term.Close()
	log.SetFlags(0)
	if logFile != nil {
		log.SetOutput(logFile)
	}
	logx.Setup(consoleEvents{term}, logFile, opt.verbose)

	store, err := config.Load(dir)
	if err != nil {
		slog.Error(fmt.Sprintf("配置无法读取：%v", err))
		term.Close()
		os.Exit(1)
	}
	proxy := mitm.New(func() config.Proxy { return store.Get().Proxy }, func() []catalog.Entry {
		cfg := store.Get()
		out := make([]catalog.Entry, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			out = append(out, catalog.Entry{ID: model.ID, DisplayName: model.DisplayName, Reasoning: model.Reasoning, Fast: model.FastSupport && model.Type == "openai-chat", ContextWindow: model.ContextWindow})
		}
		return out
	}, func(id string) (config.Model, bool) {
		cfg := store.Get()
		for _, model := range cfg.Models {
			if model.ID == id {
				model.ImageBaseURL = cfg.Image.BaseURL
				model.ImageAPIKey = cfg.Image.APIKey
				model.ImageModel = cfg.Image.Model
				return model, true
			}
		}
		return config.Model{}, false
	}, agent.NewHistory(dir))
	life := takeover.New(dir, proxy)
	application := app.New(store, life, autostart.New(), executable())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		slog.Error(fmt.Sprintf("没有可用端口：%v", err))
		term.Close()
		os.Exit(1)
	}
	url := "http://" + ln.Addr().String()
	application.SetListen(url)
	_ = fsutil.WriteFile(filepath.Join(dir, "listen.url"), []byte(url))
	term.Start(url, version, func() console.Status {
		cfg := store.Get()
		traffic := proxy.Traffic()
		st := console.Status{
			Takeover: life.Snapshot().Active, Skipped: noTakeover, Models: len(cfg.Models),
			CatalogOK: traffic.CatalogOK, CatalogN: traffic.CatalogN, CatalogAt: traffic.CatalogAt,
			Local: traffic.Local, Official: traffic.Official, LastError: traffic.LastError,
		}
		if spec, on, err := dialer.EffectiveAddress(cfg.Proxy); err == nil && on {
			st.Proxy = spec
		}
		return st
	})

	if opt.debug {
		slog.Debug(fmt.Sprintf("调试数据目录 %s", dir))
	} else if err := startWatchdog(dir); err != nil {
		slog.Warn(fmt.Sprintf("退出兜底没有挂上：%v", err))
	}

	server := &http.Server{Handler: web.Handler(application), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(ln) }()
	if !opt.debug {
		_ = openBrowser(url)
	}
	openPage := func() {
		log.Printf("打开配置页 %s", url)
		_ = openBrowser(url)
	}
	term.OnEnter(openPage)

	shutdown := func() {
		removeTrayIcon()
		application.Shutdown()
		_ = os.Remove(filepath.Join(dir, "listen.url"))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		term.Close()
	}
	defer shutdown()
	quit := func() {
		shutdown()
		os.Exit(0)
	}
	watchConsole(shutdown)
	prepareResident(term, !opt.debug, openPage, quit)
	application.OnQuit(func() {
		log.Printf("从配置页退出")
		go func() {
			time.Sleep(300 * time.Millisecond)
			quit()
		}()
	})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		quit()
	}()

	if !opt.debug {
		if store.Get().Autostart {
			if _, err := application.SyncAutostart(); err != nil {
				slog.Warn(fmt.Sprintf("开机启动写入失败：%v", err))
			}
		} else if _, err := application.SyncAutostart(); err != nil {
			slog.Warn(fmt.Sprintf("开机启动清理失败：%v", err))
		}
	}
	cfg := store.Get()
	if opt.debug {
		if err := life.ProxyOnly(); err != nil {
			slog.Error(fmt.Sprintf("调试代理没有启动：%v", err))
		} else if snap := life.Snapshot(); snap.URL != "" {
			log.Printf("调试代理 %s", snap.URL)
		}
		log.Printf("调试模式：不改 Cursor 的设置，也不结束 Cursor")
	} else if noTakeover {
		log.Printf("已跳过接管，Cursor 不会被结束")
	} else if cfg.Takeover {
		if err := application.EnableTakeover(); err != nil {
			slog.Error(fmt.Sprintf("接管失败：%v", err))
		} else {
			snap := life.Snapshot()
			if snap.URL != "" {
				log.Printf("接管代理 %s", snap.URL)
			}
			if !snap.Detail.IsZero() {
				log.Print(snap.Detail)
			}
			log.Printf("已结束 Cursor，重新打开后生效")
		}
	} else if life.ClearStale() {
		log.Printf("已清除上次留下的接管设置，并结束 Cursor")
	}
	if _, _, err := dialer.EffectiveAddress(cfg.Proxy); err != nil {
		slog.Error(fmt.Sprintf("代理地址无效：%v", err))
	}
	select {}
}
