#!/bin/sh
# 根据编译产物里实际链接的模块生成 THIRD_PARTY_NOTICES.md。
set -eu
cd "$(dirname "$0")/.."
bin=${1:-dist/cursor-inner-windows-amd64.exe}
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
	cat <<'QWEN'
## qwen-code

cursor-inner 的 Qwen 家族适配（Qwen3-Coder 的 <function=...> / <invoke> 工具调用解析、XML 实体解码、代码围栏内示例的跳过、提示词与分模型 token 上限、enable_thinking 行为）参考了 qwen-code 的设计。仅借鉴设计，未直接拷贝其源码。qwen-code 以 Apache License 2.0 发布。

cursor-inner's support for the Qwen model family (Qwen3-Coder `<function=...>` / `<invoke>` tool-call parsing, XML entity decoding, skipping format examples inside code fences, prompt tuning and per-model token limits, and the `enable_thinking` behaviour) is informed by the design of qwen-code. Only the design was studied; no source code was copied. cursor-inner's consecutive/cumulative tool-call loop guard and streamed-content repetition guard are likewise informed by qwen-code's loopDetectionService. qwen-code is released under the Apache License 2.0.

Source: https://github.com/QwenLM/qwen-code
License: Apache-2.0 (https://github.com/QwenLM/qwen-code/blob/main/LICENSE)

QWEN
	echo "## cursor-byok"
	echo
	echo "cursor-inner 的 Cursor 协议处理参考了 cursor-byok 的设计；proto/cursor/agent_v1.proto、internal/tools/tools.json 和 internal/agent/prompt.md 取自 cursor-byok。"
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
