package selfupdate

// UserMessage 是给用户看的失败说明：说发生了什么、该怎么办，不带地址和原始错误。
// 原始错误（地址已收起）只写 inner.log。配置页有自己的同义文案（page.html 的 upE_*）。
func UserMessage(code string, zh bool) string {
	pick := func(a, b string) string {
		if zh {
			return a
		}
		return b
	}
	switch code {
	case CodeNetwork:
		return pick("连不上更新源。检查网络或代理后再点“检查更新”", "Could not reach the update source. Check the network or proxy, then check for updates again")
	case CodeChecksum:
		return pick("下载的安装包校验不通过，已删除。稍后再试", "The downloaded package failed verification and was deleted. Try again later")
	case CodeNoSignature, CodeBadSignature, CodeUnknownKey, CodeExpired, CodeBadManifest:
		return pick("更新清单没有通过安全检查，已拒绝。没有安装任何文件", "The update manifest failed the security check and was rejected. Nothing was installed")
	case CodeRollback:
		return pick("更新源给出的版本比已知的旧，已忽略", "The update source offered an older version than one already seen; ignored")
	case CodeNoKey:
		return pick("此构建不能自动更新，请到 Releases 页面手动下载", "This build cannot update itself; download the new version from the Releases page")
	}
	return pick("更新没有完成。稍后再试，或点“打开日志”看原因", "The update did not finish. Try again later, or open the logs to see why")
}
