#!/bin/sh
# 根据编译产物里实际链接的模块生成 THIRD_PARTY_NOTICES.md。
set -eu
cd "$(dirname "$0")/.."
bin=${1:-dist/cursor-inner.exe}
modcache=$(go env GOMODCACHE)
goroot=$(go env GOROOT)
out=THIRD_PARTY_NOTICES.md

{
	echo "# Third-Party Notices"
	echo
	echo "cursor-inner 的发布版本包含以下第三方软件。各自的许可证原文附后。"
	echo
	echo "Release builds of cursor-inner include the third-party software listed below. Their license texts follow."
	echo
	echo "## cursor-byok"
	echo
	echo "cursor-inner 的 Cursor 协议处理（模型目录合并、BidiAppend / RunSSE 分流）参考了 cursor-byok 的设计。"
	echo
	echo "Source: https://github.com/leookun/cursor-byok"
	echo
	echo '```text'
	cat /dev/stdin
	echo '```'
	echo
	echo "## Go standard library"
	echo
	echo "Source: https://go.dev"
	echo
	echo '```text'
	cat "$goroot/LICENSE"
	echo '```'
	go version -m "$bin" | awk '$1=="dep"{print $2, $3}' | while read -r path version; do
		dir="$modcache/$(echo "$path" | sed -E 's/[A-Z]/!\L&/g')@$version"
		echo
		echo "## $path $version"
		echo
		echo "Source: https://$path"
		echo
		echo '```text'
		cat "$dir/LICENSE"
		echo '```'
	done
} > "$out" <<'BYOK'
MIT License

Copyright (c) 2026 leookun

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
BYOK
echo "$out"
