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
version=${VERSION:-}
suffix=""
if [ "$os" = windows ]; then
	suffix=".exe"
fi
# 与 Release 资源同名：cursor-inner-vX.Y.Z-windows-amd64.exe。未指定版本时省略版本段。
if [ -n "$version" ]; then
	out="dist/cursor-inner-${version}-${os}-${arch}${suffix}"
else
	out="dist/cursor-inner-${os}-${arch}${suffix}"
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
stamp=$version
if [ -z "$stamp" ]; then
	stamp=dev
fi
ldflags="-s -w -X main.version=$stamp"
# 自更新清单的签名公钥（base64 ed25519）。注入后新版本只接受带有效签名的清单。
if [ -n "${UPDATE_PUBLIC_KEY:-}" ]; then
	ldflags="$ldflags -X cursor-inner/internal/selfupdate.publicKey=$UPDATE_PUBLIC_KEY"
fi
if [ "$os" = windows ]; then
	# 窗口子系统：点控制台的叉只会关掉窗口。控制台子系统会在关闭事件返回后结束进程。
	ldflags="$ldflags -H windowsgui"
fi
go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/cursor-inner
echo "$out"
