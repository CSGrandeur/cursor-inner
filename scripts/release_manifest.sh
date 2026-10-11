#!/bin/sh
# 生成自更新清单 manifest.json 和签名 manifest.json.sig。
# 用法: scripts/release_manifest.sh <out 目录> <vX.Y.Z> <release-notes.md>
#
# 环境变量：
#   UPDATE_SIGNING_KEY       必填。ed25519 私钥 PEM 内容。客户端只认签名清单，缺了就不出清单、发版失败。
#   UPDATE_SIGNING_KEY_NEXT  可选。轮换期的第二把私钥，两把都签（客户端认其中一把即可）。
#   UPDATE_MANIFEST_TTL_DAYS 可选。写入 expires（发布后 N 天作废），防止旧清单被长期重放。
#                            设了就必须在过期前发新版，否则客户端会拒收；默认不设。
#   UPDATE_ALLOW_UNSIGNED=1  仅本地试跑：允许不签名。
set -eu
out=$1
version=$2
notes=$3
if [ -z "${UPDATE_SIGNING_KEY:-}" ] && [ "${UPDATE_ALLOW_UNSIGNED:-}" != 1 ]; then
	echo "UPDATE_SIGNING_KEY is not set: clients reject unsigned update manifests" >&2
	exit 1
fi
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
published=$(date -u +%Y-%m-%dT%H:%M:%SZ)
expires=""
if [ -n "${UPDATE_MANIFEST_TTL_DAYS:-}" ]; then
	expires=$(date -u -d "+${UPDATE_MANIFEST_TTL_DAYS} days" +%Y-%m-%dT%H:%M:%SZ)
fi
jq -n --arg v "$version" --arg pub "$published" --arg exp "$expires" --rawfile notes "$notes" --argjson assets "$assets" \
	'{schema:1, product:"cursor-inner", version:$v, published:$pub} + (if $exp == "" then {} else {expires:$exp} end) + {notes:$notes, assets:$assets}' >"$out/manifest.json"

[ -n "${UPDATE_SIGNING_KEY:-}" ] || exit 0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
sigs="[]"
i=0
for var in UPDATE_SIGNING_KEY UPDATE_SIGNING_KEY_NEXT; do
	eval "pem=\${$var:-}"
	[ -n "$pem" ] || continue
	i=$((i + 1))
	key="$tmp/key$i.pem"
	(umask 077 && printf '%s\n' "$pem" >"$key")
	# key id = sha256(32 字节原始公钥) 的前 16 个十六进制字符，与 selfupdate.KeyID 一致。
	keyid=$(openssl pkey -in "$key" -pubout -outform DER | tail -c 32 | sha256sum | cut -c1-16)
	sig=$(openssl pkeyutl -sign -inkey "$key" -rawin -in "$out/manifest.json" | base64 -w0)
	sigs=$(printf '%s' "$sigs" | jq -c --arg id "$keyid" --arg sig "$sig" '. + [{keyid:$id, sig:$sig}]')
	echo "signed manifest.json with key $keyid"
done
jq -cn --argjson s "$sigs" '{signatures:$s}' >"$out/manifest.json.sig"
