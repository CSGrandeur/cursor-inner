package logx

import (
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// 运行中的 inner.log 大小上限。发版只在启动时轮转（Rotate），个人记录版连接日志多，
// 调 SetFileCap 后运行中也会轮转：超过上限时把当前内容存成 inner.log.1（原 .1 变 .2），再把文件清空续写。
// 不改名正在写的文件：Windows 上打开着的文件改不了名，自更新交接时还有另一个进程在追加。

var (
	fileCap   atomic.Int64
	capMu     sync.Mutex
	capSince  int64
	capChecks = int64(64 << 10) // 每写这么多字节查一次大小
)

// SetFileCap 设置 inner.log 运行中的上限，0 表示不限（发版默认）。
func SetFileCap(max int64) { fileCap.Store(max) }

// noteWrite 在写完一行后调用，超过上限时轮转。
func noteWrite(w io.Writer, n int) {
	max := fileCap.Load()
	if max <= 0 {
		return
	}
	f, ok := w.(*os.File)
	if !ok {
		return
	}
	capMu.Lock()
	defer capMu.Unlock()
	capSince += int64(n)
	if capSince < capChecks {
		return
	}
	capSince = 0
	st, err := f.Stat()
	if err != nil || st.Size() < max {
		return
	}
	_ = rotateOpen(f)
}

// rotateOpen 把 f 的内容存到 .1（旧 .1 改成 .2），然后清空 f。
func rotateOpen(f *os.File) error {
	path := f.Name()
	_ = os.Remove(path + ".2")
	_ = os.Rename(path+".1", path+".2")
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(path+".1", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		src.Close()
		return err
	}
	_, err = io.Copy(dst, src)
	src.Close()
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err == nil {
		return nil
	}
	// Windows 上以追加模式打开的句柄没有写数据权限，SetEndOfFile 会被拒；另开一个可写句柄来清空，
	// 原句柄继续追加，写入落在新的文件末尾。
	w, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer w.Close()
	return w.Truncate(0)
}
