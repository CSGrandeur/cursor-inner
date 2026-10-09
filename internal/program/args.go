package program

import "strconv"

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
		case "--watch":
			if i+1 < len(args) {
				i++
				opt.watch, _ = strconv.Atoi(args[i])
			}
		}
	}
	return opt
}
