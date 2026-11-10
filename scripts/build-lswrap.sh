#!/bin/sh
# Cross-compile the Windows language_server wrapper into shim/lswrap.exe
# so the Windows build can embed it. The exe is a build artifact, not source.
set -eu
cd "$(dirname "$0")/.."
mkdir -p internal/antigravitylaunch/shim
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o internal/antigravitylaunch/shim/lswrap.exe ./internal/antigravitylaunch/lswrapcmd
