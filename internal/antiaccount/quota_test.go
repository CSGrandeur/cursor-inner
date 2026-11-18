package antiaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func overrideQuotaEndpoints(t *testing.T, loadURLs, sumURLs, modURLs []string) {
	t.Helper()
	oldLoad, oldSum, oldMod := loadCodeAssistURLs, quotaSummaryURLs, availableModelsURLs
	loadCodeAssistURLs, quotaSummaryURLs, availableModelsURLs = loadURLs, sumURLs, modURLs
	t.Cleanup(func() {
		loadCodeAssistURLs, quotaSummaryURLs, availableModelsURLs = oldLoad, oldSum, oldMod
	})
}

func TestFetchQuotaSummaryTierAndFiltersGhost3P(t *testing.T) {
	resetSoon := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	resetWeek := time.Now().Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/load":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"currentTier": map[string]any{"id": "free-tier"},
				"paidTier":    map[string]any{"id": "g1-pro-tier"},
			})
		case "/models":
			// 没有开启 Claude / GPT 模型 -> summary 里虚假的 Claude 分组应被过滤
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"gemini-3.8-pro":    map[string]any{"disabled": false},
					"claude-old":        map[string]any{"disabled": true},
					"claude-5-disabled": map[string]any{},
				},
			})
		case "/summary":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"groups": []map[string]any{
					{
						"displayName": "Gemini Models",
						"buckets": []map[string]any{
							{"bucketId": "gemini-5h", "window": "5h", "remainingFraction": 0.88, "resetTime": resetSoon},
							{"bucketId": "gemini-weekly", "window": "weekly", "remainingFraction": 0.92, "resetTime": resetWeek},
						},
					},
					{
						"displayName": "Argon Models",
						"buckets": []map[string]any{
							{"bucketId": "argon-weekly", "window": "weekly", "remainingFraction": 0.45, "resetTime": resetWeek},
						},
					},
					{
						"displayName": "Claude and GPT models",
						"buckets": []map[string]any{
							{"bucketId": "3p-5h", "window": "5h", "remainingFraction": 1.0, "resetTime": resetSoon},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	overrideQuotaEndpoints(t,
		[]string{srv.URL + "/load"},
		[]string{srv.URL + "/summary"},
		[]string{srv.URL + "/models"},
	)

	q := fetchQuota(context.Background(), srv.Client(), "tok-1", nil)
	if q == nil {
		t.Fatal("expected quota info")
	}
	if q.Tier != "PRO" || q.Forbidden {
		t.Fatalf("unexpected tier/forbidden: %+v", q)
	}
	if len(q.Buckets) != 1 || q.Buckets[0].Label != "Gemini" || q.Buckets[0].Percent != 45 || q.Buckets[0].Window != "7d" {
		t.Fatalf("expected stricter Argon weekly 45%% on Gemini and filtered ghost 3P, got %+v", q.Buckets)
	}
}

func TestFetchQuotaWeeklyZeroTakesPrecedenceAndFallbackModels(t *testing.T) {
	reset5h := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	reset7d := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)

	var sumCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/load":
			_ = json.NewEncoder(w).Encode(map[string]any{"paidTier": map[string]any{"id": "g1-ultra-tier"}})
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"claude-sonnet": map[string]any{
						"quotaInfo": map[string]any{"remainingFraction": 0.62, "resetTime": reset5h},
					},
				},
			})
		case "/summary-fail":
			http.Error(w, "busy", http.StatusServiceUnavailable)
		case "/summary":
			sumCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"groups": []map[string]any{
					{
						"displayName": "Gemini",
						"buckets": []map[string]any{
							{"bucketId": "gemini-5h", "window": "5h", "remainingFraction": 0.90, "resetTime": reset5h},
							{"bucketId": "gemini-weekly", "window": "weekly", "remainingFraction": 0.0, "resetTime": reset7d},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	overrideQuotaEndpoints(t,
		[]string{srv.URL + "/load"},
		[]string{srv.URL + "/summary-fail", srv.URL + "/summary"},
		[]string{srv.URL + "/models"},
	)

	q := fetchQuota(context.Background(), srv.Client(), "tok-1", nil)
	if q == nil || q.Tier != "ULTRA" || sumCalls.Load() != 1 {
		t.Fatalf("unexpected q=%+v sumCalls=%d", q, sumCalls.Load())
	}
	if len(q.Buckets) != 2 {
		t.Fatalf("want Gemini from summary + Claude from model fallback, got %+v", q.Buckets)
	}
	if q.Buckets[0].Label != "Gemini" || q.Buckets[0].Percent != 0 || q.Buckets[0].Window != "7d" {
		t.Fatalf("expected Gemini 0%% 7d when weekly=0, got %+v", q.Buckets[0])
	}
	if q.Buckets[1].Label != "Claude" || q.Buckets[1].Percent != 62 {
		t.Fatalf("expected Claude 62%% fallback, got %+v", q.Buckets[1])
	}
}

func TestFetchQuotaForbiddenAndNetworkPreservePrev(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/load":
			_ = json.NewEncoder(w).Encode(map[string]any{"currentTier": map[string]any{"id": "free-tier"}})
		case "/models":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/summary":
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}))
	overrideQuotaEndpoints(t,
		[]string{srv.URL + "/load"},
		[]string{srv.URL + "/summary"},
		[]string{srv.URL + "/models"},
	)

	q := fetchQuota(context.Background(), srv.Client(), "tok-1", nil)
	srv.Close()
	if q == nil || !q.Forbidden || q.Tier != "FREE" {
		t.Fatalf("want Forbidden=true Tier=FREE, got %+v", q)
	}

	// 网络全断时保留 prev 缓存快照。
	prev := &QuotaInfo{Tier: "PRO", Buckets: []QuotaBucket{{Label: "Gemini", Percent: 77}}}
	overrideQuotaEndpoints(t, []string{srv.URL + "/load"}, []string{srv.URL + "/summary"}, []string{srv.URL + "/models"})
	got := fetchQuota(context.Background(), srv.Client(), "tok-1", prev)
	if got != prev {
		t.Fatalf("expected prev pointer preserved on total network failure, got %+v", got)
	}
}
