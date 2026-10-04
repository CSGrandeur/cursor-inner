#!/bin/sh
set -eu
cd "$(dirname "$0")"

usage() {
	echo "用法: [VERSION=v0.1.0] ./build.sh <windows|linux|darwin> [amd64|arm64]" >&2
	exit 1
}

os=${1:-}
arch=${2:-amd64}
case "$os" in
win|windows) os=windows ;;
mac|darwin) os=darwin ;;
linux) os=linux ;;
*) usage ;;
esac
case "$arch" in
amd64|arm64) ;;
*) usage ;;
esac

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export CGO_ENABLED=0
export GOOS="$os"
export GOARCH="$arch"
mkdir -p dist
if [ "$os" = windows ]; then
	out="dist/cursor-inner.exe"
else
	out="dist/cursor-inner-${os}-${arch}"
fi
if [ "$os" = windows ] && [ "$arch" = amd64 ]; then
	bindir=$(go env GOBIN)
	if [ -z "$bindir" ]; then
		bindir=$(go env GOPATH)/bin
	fi
	if [ ! -x "$bindir/rsrc" ]; then
		GOOS= GOARCH= GOBIN="$bindir" go install github.com/akavel/rsrc@v0.10.2
	fi
	"$bindir/rsrc" -arch amd64 -ico assets/icon.ico -o cmd/cursor-inner/rsrc.syso
	trap 'rm -f cmd/cursor-inner/rsrc.syso' EXIT
fi
version=${VERSION:-dev}
go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out" ./cmd/cursor-inner
echo "$out"
