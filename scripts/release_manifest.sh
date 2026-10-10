#!/bin/sh
# 生成自更新清单 manifest.json（以及配置了签名密钥时的 manifest.json.sig）。
# 用法: scripts/release_manifest.sh <out 目录> <vX.Y.Z> <release-notes.md>
# 环境变量 UPDATE_SIGNING_KEY：ed25519 私钥 PEM 内容；为空则只出未签名清单。
set -eu
out=$1
version=$2
notes=$3
assets="[]"
for f in "$out"/cursor-inner-"$version"-*; do
	name=$(basename "$f")
	case "$name" in
	*-windows-amd64.exe) os=windows arch=amd64 ;;
	*-linux-amd64|*-linux-arm64|*-darwin-amd64|*-darwin-arm64)
		rest=${name#cursor-inner-"$version"-}; os=${rest%-*}; arch=${rest#*-} ;;
	*) continue ;;
	esac
	size=$(wc -c <"$f" | tr -d ' ')
	sum=$(sha256sum "$f" | cut -d' ' -f1)
	assets=$(printf '%s' "$assets" | jq -c --arg os "$os" --arg arch "$arch" --arg name "$name" --argjson size "$size" --arg sha "$sum" \
		'. + [{os:$os, arch:$arch, name:$name, size:$size, sha256:$sha}]')
done
jq -n --arg v "$version" --arg pub "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --rawfile notes "$notes" --argjson assets "$assets" \
	'{schema:1, version:$v, published:$pub, notes:$notes, assets:$assets}' >"$out/manifest.json"
if [ -n "${UPDATE_SIGNING_KEY:-}" ]; then
	key=$(mktemp)
	trap 'rm -f "$key"' EXIT
	printf '%s\n' "$UPDATE_SIGNING_KEY" >"$key"
	openssl pkeyutl -sign -inkey "$key" -rawin -in "$out/manifest.json" | base64 -w0 >"$out/manifest.json.sig"
fi
