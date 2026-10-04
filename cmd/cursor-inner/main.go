package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cursor-inner/internal/app"
	"cursor-inner/internal/autostart"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/console"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/fsutil"
	"cursor-inner/internal/mitm"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/web"
)

var version = "dev"

func main() {
	noTakeover, watchPid := parseArgs(os.Args[1:])
	if watchPid > 0 {
		takeover.RunWatch(watchPid, config.DefaultDir())
		return
	}

	release, already, err := acquireSingleton()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dir := config.DefaultDir()
	if already {
		url := ""
		if raw, err := os.ReadFile(filepath.Join(dir, "listen.url")); err == nil {
			url = strings.TrimSpace(string(raw))
			fmt.Println(url)
		}
		notifyAlreadyRunning(url)
		os.Exit(0)
	}
	defer release()

	var logFile io.Writer
	if err := os.MkdirAll(dir, 0o755); err == nil {
		if f, err := os.Create(filepath.Join(dir, "inner.log")); err == nil {
			defer f.Close()
			logFile = f
		}
	}
	term := console.New(os.Stdout, logFile)
	defer term.Close()
	log.SetFlags(0)
	log.SetOutput(term)

	store, err := config.Load(dir)
	if err != nil {
		log.Printf("配置无法读取：%v", err)
		term.Close()
		os.Exit(1)
	}
	proxy := mitm.New(func() config.Proxy { return store.Get().Proxy }, func() []catalog.Entry {
		cfg := store.Get()
		out := make([]catalog.Entry, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			out = append(out, catalog.Entry{ID: model.ID, DisplayName: model.DisplayName})
		}
		return out
	}, func(id string) (config.Model, bool) {
		for _, model := range store.Get().Models {
			if model.ID == id {
				return model, true
			}
		}
		return config.Model{}, false
	})
	life := takeover.New(dir, proxy)
	application := app.New(store, life, autostart.New(), executable())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("没有可用端口：%v", err)
		term.Close()
		os.Exit(1)
	}
	url := "http://" + ln.Addr().String()
	application.SetListen(url)
	_ = fsutil.WriteFile(filepath.Join(dir, "listen.url"), []byte(url))
	term.Start(url, version, func() console.Status {
		cfg := store.Get()
		st := console.Status{Takeover: life.Snapshot().Active, Skipped: noTakeover, Models: len(cfg.Models)}
		if spec, on, err := dialer.EffectiveAddress(cfg.Proxy); err == nil && on {
			st.Proxy = spec
		}
		return st
	})

	if err := startWatchdog(); err != nil {
		log.Printf("退出兜底没有挂上：%v", err)
	}

	server := &http.Server{Handler: web.Handler(application), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(ln) }()
	_ = openBrowser(url)

	shutdown := func() {
		application.Shutdown()
		_ = os.Remove(filepath.Join(dir, "listen.url"))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		term.Close()
	}
	defer shutdown()
	watchConsole(shutdown)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		shutdown()
		os.Exit(0)
	}()

	if store.Get().Autostart {
		if _, err := application.SyncAutostart(); err != nil {
			log.Printf("开机启动写入失败：%v", err)
		}
	} else if _, err := application.SyncAutostart(); err != nil {
		log.Printf("开机启动清理失败：%v", err)
	}
	cfg := store.Get()
	if noTakeover {
		log.Printf("已跳过接管，Cursor 不会被结束")
	} else if cfg.Takeover {
		if err := application.EnableTakeover(); err != nil {
			log.Printf("接管失败：%v", err)
		} else {
			snap := life.Snapshot()
			if snap.URL != "" {
				log.Printf("接管代理 %s", snap.URL)
			}
			if snap.Detail != "" {
				log.Print(snap.Detail)
			}
			log.Printf("已结束 Cursor，重新打开后生效")
		}
	} else if life.ClearStale() {
		log.Printf("已清除上次留下的接管设置，并结束 Cursor")
	}
	if _, _, err := dialer.EffectiveAddress(cfg.Proxy); err != nil {
		log.Printf("代理地址无效：%v", err)
	}
	select {}
}
