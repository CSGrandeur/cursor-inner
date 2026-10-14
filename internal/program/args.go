package program

import (
	"strconv"
	"strings"
)

func alreadyRunningText(url string) string {
	text := "cursor-inner 已经在运行，不会再开一份。"
	if url != "" {
		text += "\n\n配置页：\n" + url
	}
	return text
}

type options struct {
	noTakeover bool
	debug      bool
	verbose    bool
	dataDir    string
	watch      int
	handover   string
	version    bool
	background bool // 不开控制台窗口，只留通知区域图标（Windows）
	holder     int  // --console-holder 的主进程 pid
	holderFile string
	help       bool     // --help / -h
	unknown    []string // 认不出的参数：打印用法退出，不启动接管
}

func classicConsole(args []string) bool {
	for _, arg := range args {
		if arg == "--classic-console" {
			return true
		}
	}
	return false
}

func parseArgs(args []string) options {
	var opt options
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			opt.help = true
		case "--classic-console":
		case "--no-takeover":
			opt.noTakeover = true
		case "--verbose":
			opt.verbose = true
		case "--debug":
			opt.debug = true
			opt.noTakeover = true
		case "--data-dir":
			if i+1 < len(args) {
				i++
				opt.dataDir = args[i]
			}
		case "--version":
			opt.version = true
		case "--background":
			opt.background = true
		case "--console-holder":
			if i+2 < len(args) {
				opt.holder, _ = strconv.Atoi(args[i+1])
				opt.holderFile = args[i+2]
				i += 2
			}
		case "--handover":
			if i+1 < len(args) {
				i++
				opt.handover = args[i]
			}
		case "--watch":
			if i+1 < len(args) {
				i++
				opt.watch, _ = strconv.Atoi(args[i])
			}
		default:
			// macOS 旧版 Finder 会带 -psn_ 进程号参数，忽略。
			if !strings.HasPrefix(args[i], "-psn_") {
				opt.unknown = append(opt.unknown, args[i])
			}
		}
	}
	return opt
}

const usageText = `用法 / Usage: cursor-inner [选项 / options]

  --debug           调试实例：不接管 Cursor / Grok / Codex，配置页随机端口 / isolated debug instance, no takeover
  --no-takeover     不接管 / do not take over
  --data-dir <目录> 数据目录（默认用户配置目录） / data directory
  --verbose         详细日志 / verbose log
  --background      只留通知区域图标（Windows） / tray only (Windows)
  --classic-console 经典控制台窗口（Windows） / classic console window (Windows)
  --version         打印版本号 / print version
  -h, --help        打印本说明 / print this help
`

// argsExit 决定参数是否让程序直接退出：返回 -1 表示正常启动；否则返回退出码和要打印的文字。
// --help 退出码 0；认不出的参数退出码 2，绝不带着打错的参数去接管。
func argsExit(opt options) (int, string) {
	if len(opt.unknown) > 0 {
		return 2, "不认识的参数 / unknown argument: " + strings.Join(opt.unknown, " ") + "\n\n" + usageText
	}
	if opt.help {
		return 0, usageText
	}
	return -1, ""
}
