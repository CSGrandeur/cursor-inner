package main

import "strconv"

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
