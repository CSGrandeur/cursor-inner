package cursoraccount

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const quotaSubTimeout = 12 * time.Second

type profileResult struct {
	Email      string
	Name       string
	AuthID     string
	SignUpType string
	Quota      *QuotaInfo
	AuthFailed bool
}

// fetchProfileAndUsage 并发获取邮箱/登录方式、显示名、Stripe 订阅档位与当前周期用量。
func fetchProfileAndUsage(ctx context.Context, client *http.Client, accessToken string, prev *QuotaInfo) profileResult {
	var res profileResult
	if client == nil || strings.TrimSpace(accessToken) == "" {
		res.Quota = prev
		return res
	}
	qctx, cancel := context.WithTimeout(ctx, quotaSubTimeout)
	defer cancel()

	var (
		wg          sync.WaitGroup
		emailRes    struct{ email, signUp string }
		meRes       struct{ email, name, authID string }
		tier        string
		tierOK      bool
		tierAuthBad bool
		buckets     []QuotaBucket
		usageOK     bool
	)

	wg.Add(4)
	go func() {
		defer wg.Done()
		emailRes.email, emailRes.signUp = queryAuthEmail(qctx, client, accessToken)
	}()
	go func() {
		defer wg.Done()
		meRes.email, meRes.name, meRes.authID = queryDashboardMe(qctx, client, accessToken)
	}()
	go func() {
		defer wg.Done()
		tier, tierOK, tierAuthBad = queryStripeTier(qctx, client, accessToken)
	}()
	go func() {
		defer wg.Done()
		buckets, usageOK = queryCurrentUsage(qctx, client, accessToken)
	}()
	wg.Wait()

	res.Email = normalizeEmail(emailRes.email)
	if res.Email == "" {
		res.Email = normalizeEmail(meRes.email)
	}
	res.SignUpType = emailRes.signUp
	res.Name = meRes.name
	res.AuthID = meRes.authID
	res.AuthFailed = tierAuthBad

	if !tierOK && !usageOK {
		res.Quota = prev
		return res
	}
	if !tierOK && prev != nil {
		tier = prev.Tier
	}
	if !usageOK && prev != nil {
		buckets = append([]QuotaBucket(nil), prev.Buckets...)
	}
	res.Quota = &QuotaInfo{
		Tier:      tier,
		Buckets:   buckets,
		UpdatedAt: time.Now().Unix(),
	}
	return res
}

func doConnectJSON(ctx context.Context, client *http.Client, path, accessToken string) (status int, body []byte, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorBackendURL+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return 0, nil, false
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("User-Agent", "Cursor/3.30")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, nil, false
	}
	return resp.StatusCode, raw, resp.StatusCode >= 200 && resp.StatusCode < 300
}

func queryAuthEmail(ctx context.Context, client *http.Client, accessToken string) (email, signUp string) {
	_, raw, ok := doConnectJSON(ctx, client, "/aiserver.v1.AuthService/GetEmail", accessToken)
	if !ok {
		return "", ""
	}
	var doc struct {
		Email      string `json:"email"`
		SignUpType string `json:"signUpType"`
		SignUpSnk  string `json:"sign_up_type"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", ""
	}
	st := doc.SignUpType
	if st == "" {
		st = doc.SignUpSnk
	}
	return strings.TrimSpace(doc.Email), normalizeSignUpType(st)
}

func normalizeSignUpType(s string) string {
	low := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "SIGN_UP_TYPE_"))
	switch low {
	case "auth_0", "auth0":
		return "Auth_0"
	case "github":
		return "Github"
	case "google":
		return "Google"
	case "workos":
		return "WorkOS"
	case "grok":
		return "Grok"
	case "x":
		return "X"
	default:
		return ""
	}
}

func queryDashboardMe(ctx context.Context, client *http.Client, accessToken string) (email, name, authID string) {
	_, raw, ok := doConnectJSON(ctx, client, "/aiserver.v1.DashboardService/GetMe", accessToken)
	if !ok {
		return "", "", ""
	}
	var doc struct {
		AuthID    string `json:"authId"`
		AuthIDSnk string `json:"auth_id"`
		Email     string `json:"email"`
		First     string `json:"firstName"`
		FirstSnk  string `json:"first_name"`
		Last      string `json:"lastName"`
		LastSnk   string `json:"last_name"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", "", ""
	}
	aid := firstNonEmpty(doc.AuthID, doc.AuthIDSnk)
	first := firstNonEmpty(doc.First, doc.FirstSnk)
	last := firstNonEmpty(doc.Last, doc.LastSnk)
	full := strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	return strings.TrimSpace(doc.Email), full, strings.TrimSpace(aid)
}

func queryStripeTier(ctx context.Context, client *http.Client, accessToken string) (tier string, ok, authBad bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cursorBackendURL+"/auth/full_stripe_profile", nil)
	if err != nil {
		return "", false, false
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "Cursor/3.30")
	resp, err := client.Do(req)
	if err != nil {
		return "", false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", false, true
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false, false
	}
	var doc struct {
		MembershipType  string `json:"membershipType"`
		MembershipSnk   string `json:"membership_type"`
		IndividualType  string `json:"individualMembershipType"`
		IndividualSnk   string `json:"individual_membership_type"`
		SubscriptionSt  string `json:"subscriptionStatus"`
		SubscriptionSnk string `json:"subscription_status"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", false, false
	}
	rawTier := firstNonEmpty(doc.MembershipType, doc.MembershipSnk, doc.IndividualType, doc.IndividualSnk)
	if rawTier == "" {
		return "", false, false
	}
	return normalizeStripeTier(rawTier), true, false
}

func normalizeStripeTier(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ultra":
		return "ULTRA"
	case "pro_plus", "pro-plus":
		return "PRO+"
	case "pro":
		return "PRO"
	case "free_trial", "trial":
		return "TRIAL"
	case "enterprise", "team", "business":
		return "TEAM"
	case "free", "free_express", "express":
		return "FREE"
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

func stripeTierLower(tier string) string {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "ULTRA":
		return "ultra"
	case "PRO+":
		return "pro_plus"
	case "PRO":
		return "pro"
	case "TRIAL":
		return "free_trial"
	case "TEAM":
		return "enterprise"
	case "FREE":
		return "free"
	default:
		return strings.ToLower(strings.TrimSpace(tier))
	}
}

type usagePlanRaw struct {
	IncludedSpend      int      `json:"includedSpend"`
	IncludedSpendSnk   int      `json:"included_spend"`
	Limit              int      `json:"limit"`
	AutoPercentUsed    *float64 `json:"autoPercentUsed"`
	AutoPercentUsedSnk *float64 `json:"auto_percent_used"`
	APIPercentUsed     *float64 `json:"apiPercentUsed"`
	APIPercentUsedSnk  *float64 `json:"api_percent_used"`
	TotalPercentUsed   *float64 `json:"totalPercentUsed"`
	TotalPercentSnk    *float64 `json:"total_percent_used"`
}

func queryCurrentUsage(ctx context.Context, client *http.Client, accessToken string) ([]QuotaBucket, bool) {
	_, raw, ok := doConnectJSON(ctx, client, "/aiserver.v1.DashboardService/GetCurrentPeriodUsage", accessToken)
	if !ok {
		return nil, false
	}
	var doc struct {
		BillingCycleEnd    json.RawMessage `json:"billingCycleEnd"`
		BillingCycleEndSnk json.RawMessage `json:"billing_cycle_end"`
		Enabled            bool            `json:"enabled"`
		PlanUsage          *usagePlanRaw   `json:"planUsage"`
		PlanUsageSnk       *usagePlanRaw   `json:"plan_usage"`
		SpendLimit         *struct {
			IndividualUsed     int  `json:"individualUsed"`
			IndividualUsedSnk  int  `json:"individual_used"`
			IndividualLimit    *int `json:"individualLimit"`
			IndividualLimitSnk *int `json:"individual_limit"`
		} `json:"spendLimitUsage"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, false
	}
	resetAt := parseFlexUnixSec(doc.BillingCycleEnd)
	if resetAt == 0 {
		resetAt = parseFlexUnixSec(doc.BillingCycleEndSnk)
	}
	plan := doc.PlanUsage
	if plan == nil {
		plan = doc.PlanUsageSnk
	}
	var buckets []QuotaBucket
	if plan != nil {
		autoPct := firstFloatPtr(plan.AutoPercentUsed, plan.AutoPercentUsedSnk)
		apiPct := firstFloatPtr(plan.APIPercentUsed, plan.APIPercentUsedSnk)
		if autoPct != nil || apiPct != nil {
			if autoPct != nil {
				buckets = append(buckets, QuotaBucket{
					Label:   "Auto",
					Percent: clampRemainPercent(*autoPct),
					ResetAt: resetAt,
				})
			}
			if apiPct != nil {
				buckets = append(buckets, QuotaBucket{
					Label:   "API",
					Percent: clampRemainPercent(*apiPct),
					ResetAt: resetAt,
				})
			}
			return buckets, true
		}

		var usedPct float64
		hasPlan := false
		if plan.Limit > 0 {
			inc := plan.IncludedSpend
			if inc == 0 {
				inc = plan.IncludedSpendSnk
			}
			usedPct = float64(inc) / float64(plan.Limit) * 100
			hasPlan = true
		} else if total := firstFloatPtr(plan.TotalPercentUsed, plan.TotalPercentSnk); total != nil {
			usedPct = *total
			hasPlan = true
		}
		if hasPlan {
			buckets = append(buckets, QuotaBucket{
				Label:   "Plan",
				Percent: clampRemainPercent(usedPct),
				ResetAt: resetAt,
			})
			return buckets, true
		}
	}
	if doc.SpendLimit != nil {
		lim := doc.SpendLimit.IndividualLimit
		if lim == nil {
			lim = doc.SpendLimit.IndividualLimitSnk
		}
		if lim != nil && *lim > 0 {
			used := doc.SpendLimit.IndividualUsed
			if used == 0 {
				used = doc.SpendLimit.IndividualUsedSnk
			}
			usedPct := float64(used) / float64(*lim) * 100
			buckets = append(buckets, QuotaBucket{
				Label:   "Spend",
				Percent: clampRemainPercent(usedPct),
				ResetAt: resetAt,
			})
		}
	}
	return buckets, true
}

func parseFlexUnixSec(raw json.RawMessage) int64 {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0
	}
	s = strings.Trim(s, `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return 0
		}
		n = int64(f)
	}
	if n > 10_000_000_000 {
		n /= 1000
	}
	if n <= 0 {
		return 0
	}
	return n
}

func clampRemainPercent(usedPct float64) int {
	rem := int(math.Round(100 - usedPct))
	if rem < 0 {
		return 0
	}
	if rem > 100 {
		return 100
	}
	return rem
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func firstFloatPtr(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}
