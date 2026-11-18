package antiaccount

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 云端配额与订阅档位端点。独立实现的 HTTP 查询；测试可覆盖端点切片指向 httptest。
var (
	loadCodeAssistURLs = []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
	}
	quotaSummaryURLs = []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	}
	availableModelsURLs = []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
	}
)

const (
	hubUserAgent    = "antigravity/hub/2.32.2 (aidev_client; os_type=windows; arch=amd64; cl=1022991777)"
	quotaSubTimeout = 12 * time.Second
)

type modelFallbackQuota struct {
	percent int
	resetAt int64
	has     bool
}

type summaryBucketRaw struct {
	BucketID          string   `json:"bucketId"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

type summaryGroupRaw struct {
	DisplayName string             `json:"displayName"`
	Buckets     []summaryBucketRaw `json:"buckets"`
}

// fetchQuota 并发查询订阅档位（ULTRA/PRO/FREE）、模型配额分组与 403 状态。
// 三路全因网络错误失败时保留 prev，不把暂态抖动抹成空数据。
func fetchQuota(ctx context.Context, client *http.Client, accessToken string, prev *QuotaInfo) *QuotaInfo {
	if client == nil || strings.TrimSpace(accessToken) == "" {
		return prev
	}
	qctx, cancel := context.WithTimeout(ctx, quotaSubTimeout)
	defer cancel()

	var (
		tierOK    bool
		tier      string
		modelsOK  bool
		forbidden bool
		has3P     bool
		fbGemini  modelFallbackQuota
		fbClaude  modelFallbackQuota
		sumOK     bool
		groups    []summaryGroupRaw
	)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		tier, tierOK = queryTier(qctx, client, accessToken)
	}()
	go func() {
		defer wg.Done()
		modelsOK, forbidden, has3P, fbGemini, fbClaude = queryAvailableModels(qctx, client, accessToken)
	}()
	go func() {
		defer wg.Done()
		groups, sumOK = queryQuotaSummary(qctx, client, accessToken)
	}()
	wg.Wait()

	if !tierOK && !modelsOK && !sumOK && !forbidden {
		return prev
	}

	if !tierOK && prev != nil {
		tier = prev.Tier
	}
	now := time.Now().Unix()
	if forbidden {
		return &QuotaInfo{
			Tier:      tier,
			Forbidden: true,
			UpdatedAt: now,
		}
	}

	var gemini, claude *QuotaBucket
	if sumOK && len(groups) > 0 {
		gemini, claude = extractSummaryBuckets(groups, modelsOK, has3P)
	}
	if gemini == nil && fbGemini.has {
		gemini = &QuotaBucket{Label: "Gemini", Percent: fbGemini.percent, ResetAt: fbGemini.resetAt}
	}
	if claude == nil && fbClaude.has {
		claude = &QuotaBucket{Label: "Claude", Percent: fbClaude.percent, ResetAt: fbClaude.resetAt}
	}

	var buckets []QuotaBucket
	if gemini != nil {
		buckets = append(buckets, *gemini)
	}
	if claude != nil {
		buckets = append(buckets, *claude)
	}
	return &QuotaInfo{
		Tier:      tier,
		Buckets:   buckets,
		UpdatedAt: now,
	}
}

func postQuotaJSON(ctx context.Context, client *http.Client, urls []string, accessToken string, payload []byte) (status int, body []byte, ok bool) {
	for i, ep := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(payload))
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", hubUserAgent)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp.StatusCode, raw, true
		}
		if resp.StatusCode == http.StatusForbidden {
			return resp.StatusCode, raw, false
		}
		if i+1 < len(urls) && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
			continue
		}
		return resp.StatusCode, raw, false
	}
	return 0, nil, false
}

func queryTier(ctx context.Context, client *http.Client, accessToken string) (string, bool) {
	status, raw, ok := postQuotaJSON(ctx, client, loadCodeAssistURLs, accessToken, []byte(`{"metadata":{"ideType":"ANTIGRAVITY"}}`))
	if !ok || status == 0 {
		return "", false
	}
	type tierObj struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		IsDefault      bool   `json:"is_default"`
		IsDefaultCamel bool   `json:"isDefault"`
	}
	var doc struct {
		CurrentTier  *tierObj  `json:"currentTier"`
		PaidTier     *tierObj  `json:"paidTier"`
		AllowedTiers []tierObj `json:"allowedTiers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", false
	}
	rawTier := ""
	pick := func(t *tierObj) string {
		if t == nil {
			return ""
		}
		if s := strings.TrimSpace(t.ID); s != "" {
			return s
		}
		return strings.TrimSpace(t.Name)
	}
	if s := pick(doc.PaidTier); s != "" {
		rawTier = s
	} else if s := pick(doc.CurrentTier); s != "" {
		rawTier = s
	} else {
		for i := range doc.AllowedTiers {
			at := &doc.AllowedTiers[i]
			if strings.EqualFold(at.ID, "free-tier") || at.IsDefault || at.IsDefaultCamel {
				rawTier = pick(at)
				break
			}
		}
	}
	if rawTier == "" {
		rawTier = "free-tier"
	}
	low := strings.ToLower(rawTier)
	switch {
	case strings.Contains(low, "ultra"):
		return "ULTRA", true
	case strings.Contains(low, "pro"):
		return "PRO", true
	default:
		return "FREE", true
	}
}

func queryAvailableModels(ctx context.Context, client *http.Client, accessToken string) (ok, forbidden, has3P bool, fbGemini, fbClaude modelFallbackQuota) {
	status, raw, reqOK := postQuotaJSON(ctx, client, availableModelsURLs, accessToken, []byte(`{}`))
	if status == http.StatusForbidden {
		return false, true, false, fbGemini, fbClaude
	}
	if !reqOK {
		return false, false, false, fbGemini, fbClaude
	}
	var doc struct {
		Models map[string]struct {
			Disabled  bool `json:"disabled"`
			QuotaInfo *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false, false, false, fbGemini, fbClaude
	}
	for name, info := range doc.Models {
		low := strings.ToLower(strings.TrimSpace(name))
		if info.Disabled || strings.Contains(low, "-disabled") || strings.HasSuffix(low, "_disabled") {
			continue
		}
		is3P := strings.HasPrefix(low, "claude") || strings.HasPrefix(low, "gpt")
		if is3P {
			has3P = true
		}
		if info.QuotaInfo == nil || info.QuotaInfo.RemainingFraction == nil {
			continue
		}
		pct := clampPercent(*info.QuotaInfo.RemainingFraction)
		resetAt := parseExpiry(info.QuotaInfo.ResetTime)
		target := &fbGemini
		if is3P {
			target = &fbClaude
		}
		if !target.has || pct < target.percent {
			target.has = true
			target.percent = pct
			target.resetAt = resetAt
		}
	}
	return true, false, has3P, fbGemini, fbClaude
}

func queryQuotaSummary(ctx context.Context, client *http.Client, accessToken string) ([]summaryGroupRaw, bool) {
	_, raw, ok := postQuotaJSON(ctx, client, quotaSummaryURLs, accessToken, []byte(`{}`))
	if !ok {
		return nil, false
	}
	var doc struct {
		Groups []summaryGroupRaw `json:"groups"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, false
	}
	return doc.Groups, true
}

func extractSummaryBuckets(groups []summaryGroupRaw, modelsOK, has3P bool) (gemini, claude *QuotaBucket) {
	for _, g := range groups {
		if len(g.Buckets) == 0 {
			continue
		}
		gname := strings.ToLower(g.DisplayName)
		isClaude := !strings.Contains(gname, "argon") &&
			(strings.Contains(gname, "claude") || strings.Contains(gname, "gpt") || strings.Contains(gname, "3p"))
		if isClaude && modelsOK && !has3P {
			continue
		}
		chosen, win := pickGroupBucket(g.Buckets)
		if chosen == nil || chosen.RemainingFraction == nil {
			continue
		}
		label := "Gemini"
		if isClaude {
			label = "Claude"
		}
		cand := QuotaBucket{
			Label:   label,
			Percent: clampPercent(*chosen.RemainingFraction),
			ResetAt: parseExpiry(chosen.ResetTime),
			Window:  win,
		}
		if isClaude {
			claude = stricterBucket(claude, cand)
		} else {
			gemini = stricterBucket(gemini, cand)
		}
	}
	return gemini, claude
}

func pickGroupBucket(buckets []summaryBucketRaw) (chosen *summaryBucketRaw, win string) {
	var b5h, bWeek *summaryBucketRaw
	for i := range buckets {
		b := &buckets[i]
		if b.RemainingFraction == nil {
			continue
		}
		w := strings.ToLower(b.Window)
		bid := strings.ToLower(b.BucketID)
		if b5h == nil && (strings.Contains(w, "5h") || strings.Contains(bid, "5h") || strings.Contains(w, "hour") || strings.Contains(bid, "hour")) {
			b5h = b
		}
		if bWeek == nil && (strings.Contains(w, "week") || strings.Contains(bid, "week") || strings.Contains(w, "7d") || strings.Contains(bid, "7d")) {
			bWeek = b
		}
	}
	switch {
	case b5h != nil && bWeek != nil:
		if *bWeek.RemainingFraction <= 0.001 || *bWeek.RemainingFraction < *b5h.RemainingFraction {
			return bWeek, "7d"
		}
		return b5h, "5h"
	case b5h != nil:
		return b5h, "5h"
	case bWeek != nil:
		return bWeek, "7d"
	default:
		for i := range buckets {
			if buckets[i].RemainingFraction != nil {
				return &buckets[i], ""
			}
		}
		return nil, ""
	}
}

func stricterBucket(cur *QuotaBucket, cand QuotaBucket) *QuotaBucket {
	if cur == nil || cand.Percent < cur.Percent {
		return &cand
	}
	return cur
}

func clampPercent(fraction float64) int {
	p := int(math.Round(fraction * 100))
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
