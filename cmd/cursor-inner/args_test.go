package main

import "testing"

func TestParseArgs(t *testing.T) {
	no, watch := parseArgs(nil)
	if no || watch != 0 {
		t.Fatal(no, watch)
	}
	no, watch = parseArgs([]string{"--no-takeover"})
	if !no || watch != 0 {
		t.Fatal(no, watch)
	}
	no, watch = parseArgs([]string{"--watch", "42"})
	if no || watch != 42 {
		t.Fatal(no, watch)
	}
}
