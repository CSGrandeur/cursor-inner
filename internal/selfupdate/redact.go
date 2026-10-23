package selfupdate

import (
	"errors"
	"net/url"
)

// 下载地址会被 GitHub 302 到带签名查询串的临时地址（release-assets.githubusercontent.com/...?sp=...&sig=...）。
// net/http 的 *url.Error 把完整地址写进 Error()，原样进 Status.Error、控制台、托盘和 inner.log。
// 这里在发请求的地方就把地址收成 scheme://host/path：日志里还能看出是哪个主机、哪个文件，查询串、片段和账号密码都不留。

// RedactURL 只保留 scheme、主机和路径。解析不了的地址整个换成占位，不原样返回。
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(url)"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// redactErr 把 *url.Error 里的地址收起来，其余部分（操作、底层原因）不变，errors.Is/As 照常可用。
func redactErr(err error) error {
	var ue *url.Error
	if err == nil || !errors.As(err, &ue) {
		return err
	}
	return &url.Error{Op: ue.Op, URL: RedactURL(ue.URL), Err: ue.Err}
}
