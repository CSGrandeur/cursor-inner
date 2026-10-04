package console

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const headerRows = 3

type Status struct {
	Takeover bool
	Skipped  bool
	Proxy    string
	Models   int
}

type Console struct {
	mu      sync.Mutex
	out     *os.File
	file    io.Writer
	vt      bool
	url     string
	version string
	status  func() Status
	cols    int
	rows    int
	drawn   string
	closed  bool
}

func New(out *os.File, file io.Writer) *Console {
	c := &Console{out: out, file: file}
	c.vt = term.IsTerminal(int(out.Fd())) && enableVT(out)
	return c
}

func (c *Console) Start(url, version string, status func() Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.url = url
	c.version = version
	c.status = status
	if !c.vt {
		fmt.Fprintf(c.out, "cursor-inner %s  %s\n", version, url)
		return
	}
	c.cols, c.rows = c.size()
	fmt.Fprint(c.out, "\x1b[?25l\x1b[2J")
	c.layoutLocked()
	go c.watch()
}

func (c *Console) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if c.file != nil {
			fmt.Fprintf(c.file, "%s %s\n", now.Format("2006-01-02 15:04:05"), line)
		}
		if c.closed {
			continue
		}
		if c.vt {
			fmt.Fprintf(c.out, "\r\n%s%s%s  %s", faint, now.Format("15:04:05"), reset, styleLine(line))
		} else {
			fmt.Fprintf(c.out, "%s  %s\n", now.Format("15:04:05"), line)
		}
	}
	return len(p), nil
}

// OnEnter 在用户按回车时调用 fn。标准输入不是终端时不做任何事。
func (c *Console) OnEnter(fn func()) {
	in := os.Stdin
	if !term.IsTerminal(int(in.Fd())) {
		return
	}
	disableEcho(in)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			if err != nil {
				return
			}
			if bytes.ContainsAny(buf[:n], "\r\n") {
				fn()
			}
		}
	}()
}

func (c *Console) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.vt {
		fmt.Fprintf(c.out, "\x1b[r\x1b[%d;1H\r\n\x1b[?25h", c.rows)
	}
}

func (c *Console) watch() {
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		cols, rows := c.size()
		if cols != c.cols || rows != c.rows {
			c.cols, c.rows = cols, rows
			c.layoutLocked()
		} else {
			c.drawHeaderLocked(false)
		}
		c.mu.Unlock()
	}
}

func (c *Console) layoutLocked() {
	fmt.Fprintf(c.out, "\x1b[%d;%dr\x1b[%d;1H", headerRows+1, c.rows, c.rows)
	c.drawHeaderLocked(true)
}

func (c *Console) drawHeaderLocked(force bool) {
	var st Status
	if c.status != nil {
		st = c.status()
	}
	header := renderHeader(c.url, c.version, st, c.cols)
	if !force && header == c.drawn {
		return
	}
	c.drawn = header
	fmt.Fprint(c.out, "\x1b7\x1b[1;1H"+header+"\x1b8")
}

func (c *Console) size() (int, int) {
	cols, rows, err := term.GetSize(int(c.out.Fd()))
	if err != nil || cols < 20 || rows < headerRows+2 {
		return 80, 24
	}
	return cols, rows
}
