#!/bin/sh
# 把 GitHub 上刚发的版本同步成 Gitee 镜像的 Release，作为自更新的第二个更新源。
# 用法: scripts/gitee_release.sh <out 目录> <vX.Y.Z> <notes.md>
#
# 环境变量：
#   GITEE_TOKEN  Gitee 私人令牌（projects 权限）。为空则跳过，不影响 GitHub 发版。
#   GITEE_REPO   owner/repo，默认 csgrandeur/cursor-inner。
#   GITEE_USER   推送用的 Gitee 用户名，默认取 GITEE_REPO 的 owner。
#
# 只传自更新需要的文件：各平台裸可执行文件、SHA256SUMS.txt，最后才传 manifest.json.sig 和 manifest.json——
# 客户端在 Gitee 上看到新清单时，安装包必须已经在了；在那之前 Gitee 这个源只是查不到新版本。
# 完整性由签名清单保证：Gitee 上的文件若被替换，只会被 sha256 拒收，客户端换回 GitHub 下载。
# 可以重跑：Release 已存在就复用，同名附件已存在就跳过。
set -eu
out=$1
version=$2
notes=$3
if [ -z "${GITEE_TOKEN:-}" ]; then
	echo "::notice::GITEE_TOKEN is not set; skipping the Gitee mirror"
	exit 0
fi
repo=${GITEE_REPO:-csgrandeur/cursor-inner}
user=${GITEE_USER:-${repo%%/*}}
api="https://gitee.com/api/v5/repos/$repo"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# 令牌只从文件读进请求体或请求头，不出现在命令行参数和远端地址里。
(umask 077 && printf '%s' "$GITEE_TOKEN" >"$tmp/token")

# 1. 代码和 tag 推到 Gitee（Release 必须挂在已有的 tag 上；checkout 需要完整历史）。
auth=$(printf '%s:%s' "$user" "$GITEE_TOKEN" | base64 -w0)
(umask 077 && printf '[http]\n\textraheader = Authorization: Basic %s\n' "$auth" >"$tmp/gitconfig")
GIT_CONFIG_GLOBAL="$tmp/gitconfig" git push --force "https://gitee.com/$repo.git" "HEAD:refs/heads/main" "refs/tags/$version:refs/tags/$version"

# 2. 建 Release，已存在就复用。
id=$(curl -fsS "$api/releases/tags/$version" 2>/dev/null | jq -r '.id // empty' || true)
if [ -z "$id" ]; then
	pre=false
	case "$version" in *-*) pre=true ;; esac
	id=$(curl -fsS -X POST "$api/releases" \
		--data-urlencode "access_token@$tmp/token" \
		--data-urlencode "tag_name=$version" \
		--data-urlencode "name=cursor-inner $version" \
		--data-urlencode "body@$notes" \
		--data-urlencode "prerelease=$pre" \
		--data-urlencode "target_commitish=main" | jq -r '.id // empty')
fi
[ -n "$id" ] || { echo "::error::could not create the Gitee release $version"; exit 1; }

# 3. 传文件，清单最后。
have=$(curl -fsS "$api/releases/$id/attach_files?per_page=100" 2>/dev/null | jq -r '.[].name' || true)
for f in "$out/cursor-inner-$version-windows-amd64.exe" \
	"$out/cursor-inner-$version-linux-amd64" "$out/cursor-inner-$version-linux-arm64" \
	"$out/cursor-inner-$version-darwin-amd64" "$out/cursor-inner-$version-darwin-arm64" \
	"$out/SHA256SUMS.txt" "$out/manifest.json.sig" "$out/manifest.json"; do
	name=$(basename "$f")
	if printf '%s\n' "$have" | grep -qxF "$name"; then
		echo "already on Gitee: $name"
		continue
	fi
	curl -fsS -X POST "$api/releases/$id/attach_files" -F "access_token=<$tmp/token" -F "file=@$f" >/dev/null
	echo "uploaded $name"
done

# 4. 自检：Gitee 的「最新版本」API 要指向这个 tag，客户端靠它找清单。
latest=$(curl -fsS "$api/releases/latest" | jq -r '.tag_name // empty' || true)
if [ "$latest" != "$version" ]; then
	echo "::warning::Gitee latest release is '$latest', not $version (a prerelease is never 'latest')"
fi
