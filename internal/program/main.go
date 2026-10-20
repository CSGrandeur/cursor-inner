package program

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
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
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/logx"
	"cursor-inner/internal/mitm"
	"cursor-inner/internal/selfupdate"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/updateview"
	"cursor-inner/internal/web"
)

type consoleEvents struct{ c *console.Console }

func (e consoleEvents) Write(p []byte) (int, error) {
	e.c.Event(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func rejectIfRunning(dir string) bool {
	release, already, err := acquireSingleton(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !already {
		release()
		return false
	}
	notifyAlreadyRunning(listenURL(dir))
	os.Exit(0)
	return true
}

func listenURL(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "listen.url"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func Main(version string) {
	windowTitle = web.Title(version)
	opt := parseArgs(os.Args[1:])
	if code, text := argsExit(opt); code >= 0 {
		out := os.Stdout
		if code != 0 {
			out = os.Stderr
		}
		fmt.Fprint(out, text)
		os.Exit(code)
	}
	if opt.version {
		// 自更新的预检：只报版本号，不碰任何状态。
		fmt.Println(version)
		return
	}
	if opt.holder > 0 {
		runConsoleHolder(opt.holder, opt.holderFile)
		return
	}
	if opt.watch > 0 {
		dir := opt.dataDir
		if dir == "" {
			dir = config.DefaultDir()
		}
		takeover.RunWatch(opt.watch, dir)
		return
	}

	dir := opt.dataDir
	if dir == "" && !opt.debug {
		dir = config.DefaultDir()
	}
	// 已在运行时直接提示并退出，不要再为换控制台拉起第二个进程，否则会弹出两份提示。
	var succ *selfupdate.Successor
	if opt.handover != "" {
		s, err := selfupdate.JoinHandover(opt.handover)
		if err != nil {
			os.Exit(3)
		}
		succ = s
	}
	if succ == nil && dir != "" && rejectIfRunning(dir) {
		return
	}
	// 后台运行：不开控制台窗口，只留通知区域图标（只有 Windows 有托盘）。交接时旧进程用 --background 把
	// “控制台此刻收着”传过来；否则看配置。
	background := traySupported && (opt.background || peekBackground(dir))
	if !background {
		if handoffToClassicConsole() {
			return
		}
		bindConsoleIO()
	}

	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "cursor-inner-debug-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	noTakeover := opt.noTakeover
	if succ != nil {
		// 交接：先告诉旧进程我能启动，等它交出监听和单实例。旧进程取消或超时就直接退出，旧进程照常服务。
		if err := succ.Ready(os.Getpid()); err != nil {
			os.Exit(3)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := succ.WaitReleased(ctx)
		cancel()
		if err != nil {
			os.Exit(3)
		}
	}
	release, already, err := acquireSingletonRetry(dir, succ != nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if already {
		notifyAlreadyRunning(listenURL(dir))
		os.Exit(0)
	}
	defer release()
	brandConsoleWindow()
	mitm.NotifyReady(dir)

	var logFile io.Writer
	if err := os.MkdirAll(dir, 0o755); err == nil {
		logPath := filepath.Join(dir, "inner.log")
		if f, err := openInnerLog(logPath, succ != nil); err == nil {
			defer f.Close()
			logFile = f
		}
	}
	term := console.New(os.Stdout, logFile)
	defer term.Close()
	term.SetLiveProgress(os.Getenv("CURSOR_INNER_LIVE_PROGRESS") == "1")
	if background {
		term.Detach()
		concealConsoleAtStart()
	}
	log.SetFlags(0)
	if logFile != nil {
		log.SetOutput(logFile)
	}
	logx.Setup(consoleEvents{term}, logFile, opt.verbose)
	if opt.debug || opt.verbose {
		grokbot.SetConnDebug(true)
	}

	store, err := config.Load(dir)
	if err != nil {
		slog.Error(fmt.Sprintf("配置无法读取：%v", err))
		term.Close()
		os.Exit(1)
	}
	if opt.debug && dir != config.DefaultDir() {
		if src, ok := config.Peek(config.DefaultDir()); ok {
			if spec := inheritProxy(store, src); spec != "" {
				log.Printf("调试模式沿用配置的出站代理 %s", spec)
			}
		}
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
	if succ != nil {
		application.SetTakeoverPhase(takeover.PhaseHandover) // 接入中：接管没接上之前是“正在切换”，不是故障
	}
	if opt.debug {
		application.Isolate()
	}

	webAddr := "127.0.0.1:0"
	if succ != nil && succ.Plan.WebAddr != "" {
		webAddr = succ.Plan.WebAddr
	}
	ln, err := listenWeb(webAddr)
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
	} else if succ != nil {
		// 交接：守护进程等旧进程确认之后再挂（见 Successor.WaitConfirmed）。
	} else if err := startWatchdog(dir); err != nil {
		slog.Warn(fmt.Sprintf("退出兜底没有挂上：%v", err))
	}

	server := &http.Server{Handler: web.HandlerVersion(application, version), ReadHeaderTimeout: 10 * time.Second}
	server.RegisterOnShutdown(func() { web.CloseStreams(server.Handler) })
	webRL := &relisten{srv: server, ln: ln}
	go func() { _ = server.Serve(ln) }()
	upd := newUpdateGlue(version, dir, opt.debug, store, life, proxy, application, webRL, &release, term)
	hub := newTrayHub(version, opt.debug, uiChinese())
	hub.status = upd.updater.Status
	hub.alerts = application.Alerts
	hub.traffic = func() (int, string) { t := proxy.Traffic(); return t.Leaks, t.LastLeak }
	hub.quiet = func() bool { return store.Get().Desktop.Quiet }
	hub.send = trayBalloon
	application.SetUpdater(upd.updater)
	go updateview.Run(context.Background(), upd.updater, term)
	if !opt.debug && succ == nil {
		_ = openBrowser(url)
	}
	openPage := func(page string) {
		target := url
		if page != "" {
			target += "#" + page
		}
		log.Printf("打开配置页 %s", target)
		_ = openBrowser(target)
	}
	setTrayUpdate(func() { upd.trayCheck(openPage) })
	term.OnEnter(func() { openPage("") })
	installConsoleLink(term, func() { openPage("") })

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
	// 托盘“退出”：有程序会被一并关掉且没选过不再询问时，打开配置页的确认框；Ctrl+C、注销、关机不问。
	trayQuit := func() {
		if application.QuitPlan().Confirm {
			openPage("quit")
			return
		}
		quit()
	}
	if prepareResident(residentOptions{
		Term:  term,
		Open:  openPage,
		Quit:  trayQuit,
		Feed:  func() trayInput { return hub.input(time.Now()) },
		Quiet: hub.quiet,
		Defer: succ != nil,
	}) {
		application.SetTray(func(d config.Desktop) {
			if d.Background {
				hideConsole()
			} else {
				go showConsole()
			}
		})
		go runTrayHub(hub, upd)
	} else if background {
		showConsole() // 图标没挂上：不能什么界面都没有
	}
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
		for range sig {
			if consoleHidden() {
				continue
			}
			quit()
		}
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
	if opt.debug && succ != nil && succ.Plan.MitmAddr != "" {
		if err := life.ProxyOnlyAt(succ.Plan.MitmAddr); err != nil {
			slog.Error(fmt.Sprintf("调试代理交接失败：%v", err))
			os.Exit(4)
		}
		log.Printf("调试模式：已接过调试代理 %s", life.Snapshot().URL)
	} else if opt.debug {
		if err := life.ProxyOnly(); err != nil {
			slog.Error(fmt.Sprintf("调试代理没有启动：%v", err))
		} else if snap := life.Snapshot(); snap.URL != "" {
			log.Printf("调试代理 %s", snap.URL)
		}
		log.Printf("调试模式：不改 Cursor 的设置，也不结束 Cursor；不关闭或重开 Grok Bot")
	} else if succ != nil {
		if succ.Plan.TakeoverActive {
			if err := life.Adopt(succ.Plan.MitmAddr); err != nil {
				slog.Error(fmt.Sprintf("交接接管失败：%v", err))
				os.Exit(4) // 旧进程会回滚并收回端口
			}
			log.Printf("已从 %s 接过接管 %s，Cursor 与 Grok 无需重启", succ.Plan.FromVersion, life.Snapshot().URL)
		}
	} else if noTakeover {
		application.SetTakeoverSkipped(true)
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
	// 启动 / 交接流程走完：从这里起接管没生效才算“真的没生效”（见 takeover.Tracker）。
	application.SetTakeoverPhase(takeover.PhaseSettled)
	if _, _, err := dialer.EffectiveAddress(cfg.Proxy); err != nil {
		slog.Error(fmt.Sprintf("代理地址无效：%v", err))
	}
	if note := mitm.TraceNote(); note != "" {
		log.Printf("行为记录 %s", note)
	}
	if !opt.debug && succ == nil {
		if names := application.QuitPlan().Restore; len(names) > 0 {
			go func() {
				time.Sleep(3 * time.Second) // 等托盘图标挂上
				if !hub.quiet() {
					trayBalloon(restoreBalloon(uiChinese(), names))
				}
			}()
		}
	}
	if !opt.debug {
		application.SyncGrok()
		application.SyncCodex()
		go func() {
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for range tick.C {
				application.SyncGrok()
				application.SyncCodex()
			}
		}()
	}
	if succ != nil {
		if err := succ.Done(selfupdate.Ack{PID: os.Getpid(), Version: version, WebURL: url}); err != nil {
			os.Exit(4)
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := succ.WaitConfirmed(ctx, os.Getpid()); err != nil {
				return // 旧进程会结束本进程并回滚
			}
			// 交接确认之后才算更新成功（之前旧进程还可能回滚）。控制台打一行总结，日志记一条。
			slog.Info("已更新到 "+version, "from", succ.Plan.FromVersion, logx.FileOnly)
			term.Line(fmt.Sprintf("✓ 已从 %s 热交接到 %s", succ.Plan.FromVersion, version))
			// 旧进程确认后才显示图标：旧进程这时正删掉它的图标，通知区域里始终只有一个。
			showTrayIcon()
			if !hub.quiet() {
				trayBalloon(updatedBalloon(uiChinese(), opt.debug, succ.Plan.FromVersion, version))
			}
			if !opt.debug {
				if err := startWatchdog(dir); err != nil {
					slog.Warn(fmt.Sprintf("退出兜底没有挂上：%v", err))
				}
			}
		}()
	}
	upd.cleanupOld(succ != nil)
	select {}
}

// peekBackground 只读地看配置里有没有打开“后台运行”。
func peekBackground(dir string) bool {
	if dir == "" {
		return false
	}
	f, ok := config.Peek(dir)
	return ok && f.Desktop.Background
}

// runTrayHub 给托盘喂数据：订阅更新快照（失败时弹通知、图标马上变），每 2 秒看一次告警和泄漏计数。
func runTrayHub(hub *trayHub, upd *updateGlue) {
	go func() {
		w := upd.updater.Watch()
		defer w.Close()
		for {
			st, err := w.Next(context.Background())
			if err != nil {
				return
			}
			hub.onUpdate(st)
			trayKick()
		}
	}()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for now := range tick.C {
		hub.poll(now)
	}
}

// openInnerLog 打开本次运行的 inner.log，一律追加、不清空：重启前的断连等记录要能在重启后查到。
// 超过 5MB 时先轮转成 inner.log.1（logx.Rotate），历史不会无限增长。
// 用追加模式：自更新交接时新旧两个进程同时在写，追加模式下每次写都落在文件末尾，不会互相覆盖。
func openInnerLog(path string, handover bool) (*os.File, error) {
	if !handover {
		_ = logx.Rotate(path)
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
