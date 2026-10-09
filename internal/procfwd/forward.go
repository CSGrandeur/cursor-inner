package procfwd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
)

// Forwarder 在本机 443 上接下子进程对名单内主机的直连，再按原主机名从自定义代理拨出去。
type Forwarder struct {
	dial  dialer.Func
	port  string
	path  string
	flush bool

	mu sync.Mutex
	ln net.Listener
}

func New(dial dialer.Func) *Forwarder {
	return &Forwarder{dial: dial, port: "443", flush: true}
}

func (f *Forwarder) file() string {
	if f.path != "" {
		return f.path
	}
	return hostsPath()
}

// Sync 在自定义代理开着时写上 hosts 并开始转发，关掉时删掉。
func (f *Forwarder) Sync(on bool) error {
	if !on {
		return f.Stop()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.listenLocked(); err != nil {
		return err
	}
	if err := installHosts(f.file()); err != nil {
		if !hostsHeld(f.file()) {
			f.closeLocked()
		}
		return i18n.Wrap("没能改系统 hosts，Cursor 子进程的直连进不了自定义代理：", "Could not edit the system hosts file, so Cursor child processes that connect directly stay outside the proxy: ", err)
	}
	if f.flush {
		flushDNS()
	}
	return nil
}

// Listen 只在本机 443 接着。hosts 还指着本机、又删不掉时用它，避免这几个名字中断。
func (f *Forwarder) Listen() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listenLocked()
}

func (f *Forwarder) listenLocked() error {
	if f.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:"+f.port)
	if err != nil {
		return i18n.Wrap("没能在本机 443 端口接收 Cursor 子进程的直连：", "Could not listen on local port 443 for Cursor child processes that connect directly: ", err)
	}
	f.ln = ln
	go f.accept(ln)
	return nil
}

// Stop 先删 hosts，确认删掉后再关 443。删不掉就继续听着。
func (f *Forwarder) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := removeHosts(f.file()); err != nil {
		return i18n.Wrap("没能从系统 hosts 去掉 Cursor 子进程的直连转发：", "Could not remove the Cursor child-process redirect from the system hosts file: ", err)
	}
	if hostsHeld(f.file()) {
		return i18n.E("没能从系统 hosts 去掉 Cursor 子进程的直连转发", "Could not remove the Cursor child-process redirect from the system hosts file")
	}
	if f.flush {
		flushDNS()
	}
	f.closeLocked()
	return nil
}

func (f *Forwarder) closeLocked() {
	if f.ln != nil {
		_ = f.ln.Close()
		f.ln = nil
	}
}

func (f *Forwarder) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go f.serve(conn)
	}
}

func (f *Forwarder) serve(conn net.Conn) {
	defer conn.Close()
	name, raw, err := readHello(conn)
	target := net.JoinHostPort(name, "443")
	if err != nil || !allowed(name) {
		reason := "not forwarded"
		if err != nil {
			reason = err.Error()
		}
		if name == "" {
			target = "unknown"
		}
		noteTap(target, "child", reason)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	up, err := f.dial(ctx, "tcp", target)
	cancel()
	if err != nil {
		slog.Warn(fmt.Sprintf("子进程直连 %s 没能从自定义代理拨出：%v", name, err))
		noteTap(target, "child", err.Error())
		return
	}
	defer up.Close()
	if _, err = up.Write(raw); err != nil {
		noteTap(target, "child", err.Error())
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	toNet, fromNet := openTap(target, "child")
	if toNet != nil {
		_, _ = io.Copy(steadyWriter{toNet}, bytes.NewReader(raw))
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(up, tapReader(conn, toNet))
		closeTap(toNet)
		_ = up.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, tapReader(up, fromNet))
		closeTap(fromNet)
		_ = conn.Close()
		done <- struct{}{}
	}()
	<-done
}
