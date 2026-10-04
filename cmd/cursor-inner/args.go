package main

import "strconv"

func alreadyRunningText(url string) string {
	text := "cursor-inner 已经在运行，不会再开一份。"
	if url != "" {
		text += "\n\n配置页：\n" + url
	}
	return text
}

func parseArgs(args []string) (noTakeover bool, watch int) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-takeover":
			noTakeover = true
		case "--watch":
			if i+1 < len(args) {
				i++
				watch, _ = strconv.Atoi(args[i])
			}
		}
	}
	return
}
