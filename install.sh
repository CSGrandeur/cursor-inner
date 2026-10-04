#!/bin/sh
# cursor-inner installer for macOS and Linux.
#   curl -fsSL https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.sh | sh
# Environment: CURSOR_INNER_VERSION=v0.1.0 to pin a release, CURSOR_INNER_BIN=/path for the install dir.
set -eu

repo="CSGrandeur/cursor-inner"
bin_dir="${CURSOR_INNER_BIN:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
fail() { printf 'cursor-inner: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported OS $(uname -s) / 不支持的系统" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported CPU $(uname -m) / 不支持的 CPU 架构" ;;
esac
command -v curl >/dev/null || fail "curl is required / 需要 curl"

version="${CURSOR_INNER_VERSION:-}"
if [ -z "$version" ]; then
	latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")
	version="${latest##*/}"
fi
case "$version" in v*) ;; *) fail "cannot determine the latest release / 找不到最新版本" ;; esac

name="cursor-inner-$version-$os-$arch"
base="https://github.com/$repo/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "Downloading / 下载 $name.tar.gz"
curl -fsSL "$base/$name.tar.gz" -o "$tmp/$name.tar.gz"
curl -fsSL "$base/SHA256SUMS.txt" -o "$tmp/SHA256SUMS.txt"

expected=$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/SHA256SUMS.txt")
if command -v sha256sum >/dev/null; then
	actual=$(sha256sum "$tmp/$name.tar.gz" | awk '{ print $1 }')
else
	actual=$(shasum -a 256 "$tmp/$name.tar.gz" | awk '{ print $1 }')
fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "checksum mismatch / 校验和不一致"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
mkdir -p "$bin_dir"
install -m 755 "$tmp/$name/cursor-inner" "$bin_dir/cursor-inner"
if [ "$os" = darwin ]; then
	xattr -d com.apple.quarantine "$bin_dir/cursor-inner" 2>/dev/null || true
fi

if [ "$os" = linux ]; then
	data="${XDG_DATA_HOME:-$HOME/.local/share}"
	mkdir -p "$data/icons/hicolor/scalable/apps" "$data/applications"
	cp "$tmp/$name/cursor-inner.svg" "$data/icons/hicolor/scalable/apps/cursor-inner.svg"
	cat >"$data/applications/cursor-inner.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=cursor-inner
Comment=Bring your own models into Cursor
Exec="$bin_dir/cursor-inner"
Icon=cursor-inner
Terminal=true
Categories=Development;
EOF
fi

say "Installed / 已安装: $bin_dir/cursor-inner ($version)"
case ":$PATH:" in
*":$bin_dir:"*) say "Run / 运行: cursor-inner" ;;
*) say "Run / 运行: $bin_dir/cursor-inner   (add $bin_dir to PATH to run it as cursor-inner)" ;;
esac
