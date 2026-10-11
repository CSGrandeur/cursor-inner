package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Source 是一个更新源。清单和安装包的完整性只靠签名清单里的 sha256，所以多加一个镜像不会降低安全性：
// 镜像能做的最坏的事是不给更新（由过期时间和回滚检查兜底），或者给坏文件（被 sha256 拒掉、换下一个源）。
type Source struct {
	Name string // 显示用：github、gitee
	// URL 是固定的清单地址（GitHub 的 releases/latest/download/manifest.json 走重定向，不调 API、不受匿名限流）。
	URL string
	// LatestAPI 和 DownloadBase 给没有「最新版」固定地址的源（Gitee）：先从 API 取最新 tag，
	// 再拼 DownloadBase + tag + "/manifest.json"。tag 必须是版本号。
	LatestAPI    string
	DownloadBase string
}

// DefaultSources 并行查询，取验签通过的最高版本；版本相同时用先返回的那个，下载失败再换另一个。
var DefaultSources = []Source{
	{Name: "github", URL: "https://github.com/CSGrandeur/cursor-inner/releases/latest/download/manifest.json"},
	{Name: "gitee", LatestAPI: "https://gitee.com/api/v5/repos/csgrandeur/cursor-inner/releases/latest", DownloadBase: "https://gitee.com/csgrandeur/cursor-inner/releases/download/"},
}

// URLSource 用一个固定清单地址做源（调试演练用）。
func URLSource(raw string) Source {
	name := raw
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		name = u.Host
	}
	return Source{Name: name, URL: raw}
}

var errBadManifest = errors.New("更新源返回的内容无效")

// sourceResult 是一个源这次检查的结果。
type sourceResult struct {
	src      Source
	route    Route
	manURL   string
	manifest Manifest
	keyID    string
	elapsed  time.Duration
	err      error
}

// SourceStatus 是给界面看的每个源的结果。
type SourceStatus struct {
	Name    string `json:"name"`
	Route   string `json:"route,omitempty"`
	Version string `json:"version,omitempty"`
	Millis  int64  `json:"ms"`
	Error   string `json:"error,omitempty"`
	Code    string `json:"code,omitempty"`
	Chosen  bool   `json:"chosen,omitempty"`
}

// fetchSource 取一个源的清单和签名并验签。清单、签名、之后的下载都走同一条网络路径。
func (u *Updater) fetchSource(ctx context.Context, src Source, routes []Route) sourceResult {
	start := time.Now()
	res := sourceResult{src: src}
	manURL := src.URL
	var got Fetched
	var err error
	if manURL == "" {
		var api Fetched
		api, _, err = FetchFirst(ctx, routes, src.LatestAPI, u.opt.Timeout, 1<<20)
		if err != nil {
			res.err = err
			res.elapsed = time.Since(start)
			return res
		}
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if jerr := json.Unmarshal(api.Body, &rel); jerr != nil {
			res.err, res.elapsed = fmt.Errorf("%w：最新版本信息无法解析", errBadManifest), time.Since(start)
			return res
		}
		if _, ok := ParseVersion(rel.TagName); !ok || strings.ContainsAny(rel.TagName, "/\\?#%") {
			res.err, res.elapsed = fmt.Errorf("%w：最新版本的 tag %q 不是版本号", errBadManifest, rel.TagName), time.Since(start)
			return res
		}
		manURL = src.DownloadBase + rel.TagName + "/manifest.json"
		body, final, ferr := fetch(ctx, api.Route, manURL, u.opt.Timeout, 1<<20)
		if ferr != nil {
			res.err, res.elapsed = ferr, time.Since(start)
			return res
		}
		got = Fetched{Body: body, FinalURL: final, Route: api.Route}
	} else {
		got, _, err = FetchFirst(ctx, routes, manURL, u.opt.Timeout, 1<<20)
		if err != nil {
			res.err, res.elapsed = err, time.Since(start)
			return res
		}
	}
	res.route, res.manURL = got.Route, got.FinalURL
	var sig []byte
	if len(u.opt.Keys) > 0 {
		s, _, serr := fetch(ctx, got.Route, sigURL(got.FinalURL), u.opt.Timeout, 16<<10)
		if serr != nil && !errors.Is(serr, errNotFound) {
			res.err, res.elapsed = fmt.Errorf("取签名失败：%w", serr), time.Since(start)
			return res
		}
		sig = s // 404 时为空，ParseManifest 报「没有签名」
	}
	m, keyID, perr := ParseManifest(got.Body, sig, u.opt.Keys, u.now())
	if perr != nil {
		if ErrorCode(perr) == "" {
			perr = fmt.Errorf("%w：%v", errBadManifest, perr)
		}
		res.err, res.elapsed = perr, time.Since(start)
		return res
	}
	res.manifest, res.keyID, res.elapsed = m, keyID, time.Since(start)
	return res
}

// pickBest 取验过的最高版本；同版本时取最快的。返回选中的下标，没有可用的返回 -1。
func pickBest(results []sourceResult) int {
	best := -1
	for i, r := range results {
		if r.err != nil {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		v, _ := ParseVersion(r.manifest.Version)
		b, _ := ParseVersion(results[best].manifest.Version)
		if c := Compare(v, b); c > 0 || (c == 0 && r.elapsed < results[best].elapsed) {
			best = i
		}
	}
	return best
}

// mirrorsFor 给出下载这个版本的所有地方：选中的源排第一，其余同版本、同一份文件（sha256 相同）的源按速度排在后面。
func mirrorsFor(results []sourceResult, best int, a Asset) []Mirror {
	order := []int{best}
	for i, r := range results {
		if i == best || r.err != nil || r.manifest.Version != results[best].manifest.Version {
			continue
		}
		if other, err := r.manifest.Pick(a.OS, a.Arch); err != nil || !strings.EqualFold(other.SHA256, a.SHA256) || other.Size != a.Size {
			continue
		}
		order = append(order, i)
	}
	for i := 1; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			if results[order[j]].elapsed < results[order[i]].elapsed {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	var out []Mirror
	for _, i := range order {
		r := results[i]
		pa, _ := r.manifest.Pick(a.OS, a.Arch)
		out = append(out, Mirror{Name: r.src.Name, Route: r.route, URLs: assetURLs(pa, r.manURL)})
	}
	return out
}
