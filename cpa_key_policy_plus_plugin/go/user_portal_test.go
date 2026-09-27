package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

func plusSessionCookie(t *testing.T, rawKey string) string {
	t.Helper()
	raw, err := userSession(managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{rawKey}}})
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	for _, rawCookie := range resp.Headers.Values("Set-Cookie") {
		if strings.Contains(rawCookie, "Path=/;") {
			return strings.SplitN(rawCookie, ";", 2)[0]
		}
	}
	t.Fatalf("root session cookie not found: %#v", resp.Headers)
	return ""
}

func insertPortalUsage(t *testing.T, key policyplus.KeyRecord, requestID string, at time.Time, cost float64, pending bool) {
	t.Helper()
	event := policyplus.UsageEvent{
		RequestID:      requestID,
		KeyID:          key.ID,
		KeyPreview:     key.Preview,
		Model:          "gpt-5.5",
		RequestedAt:    at,
		Cost:           cost,
		BillingPending: pending,
	}
	if pending {
		event.BillingReason = "model_price_unavailable"
	}
	if err := loadedStore().InsertUsage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func portalGet(t *testing.T, path, cookie string, query url.Values) managementResponse {
	t.Helper()
	req := managementRequest{Method: http.MethodGet, Path: path, Query: query, Headers: http.Header{}}
	if cookie != "" {
		req.Headers.Set("Cookie", cookie)
	}
	raw, err := managementHandle(mustJSON(t, req))
	if err != nil {
		t.Fatal(err)
	}
	return decodeManagementResponse(t, raw)
}

func TestUserPortalRoutesAreRegistered(t *testing.T) {
	raw, err := managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var reg managementRegistrationResponse
	unwrapPlusEnvelope(t, raw, &reg)
	registered := map[string]bool{}
	for _, resource := range reg.Resources {
		registered[resource.Path] = true
	}
	for _, want := range []string{"/user/api/logout", "/user/api/trend"} {
		if !registered[want] {
			t.Fatalf("resource %s must be registered so CPA forwards it: %#v", want, reg.Resources)
		}
	}
}

func TestUserLogoutExpiresEverySessionCookie(t *testing.T) {
	setupTestState(t)
	resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/logout", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d body=%s", resp.StatusCode, resp.Body)
	}
	cookies := resp.Headers.Values("Set-Cookie")
	joined := strings.Join(cookies, "\n")
	for _, name := range []string{plusSessionCookieName, "cpa_governor_session"} {
		for _, path := range userSessionCookiePaths {
			if !strings.Contains(joined, name+"=; Path="+path+"; Max-Age=0;") {
				t.Fatalf("logout must expire %s on %s: %#v", name, path, cookies)
			}
		}
	}
	if len(cookies) != 2*len(userSessionCookiePaths) {
		t.Fatalf("unexpected logout cookies: %#v", cookies)
	}
}

func TestUserTrendBucketsAddUpToQuotaWindow(t *testing.T) {
	key := setupTestState(t)
	now := time.Now()
	insertPortalUsage(t, key, "req-outside", now.Add(-30*time.Hour), 5, false)
	insertPortalUsage(t, key, "req-edge", now.Add(-23*time.Hour-30*time.Minute), 0.4, false)
	insertPortalUsage(t, key, "req-mid", now.Add(-3*time.Hour), 0.25, false)
	insertPortalUsage(t, key, "req-recent", now.Add(-10*time.Minute), 0.1, false)
	insertPortalUsage(t, key, "req-pending", now.Add(-5*time.Minute), 9, true)
	cookie := plusSessionCookie(t, "sk-alice-secret")

	resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/trend", cookie, url.Values{"range": {"24h"}, "tz_offset": {"-480"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trend status = %d body=%s", resp.StatusCode, resp.Body)
	}
	var body struct {
		OK    bool                   `json:"ok"`
		Trend policyplus.UsageSeries `json:"trend"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	want, err := loadedStore().UsageSum(context.Background(), key.ID, policyplus.WindowFor(policyplus.Range24H, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	sum, calls, pending := 0.0, int64(0), int64(0)
	for _, bucket := range body.Trend.Buckets {
		sum += bucket.Cost
		calls += bucket.Calls
		pending += bucket.BillingPending
	}
	if math.Abs(want-0.75) > 1e-9 || math.Abs(sum-want) > 1e-9 || math.Abs(body.Trend.TotalCost-want) > 1e-9 {
		t.Fatalf("trend must add up to the 24h quota window: buckets=%.6f total=%.6f window=%.6f", sum, body.Trend.TotalCost, want)
	}
	if calls != 4 || pending != 1 {
		t.Fatalf("trend counts calls=%d pending=%d, want 4 and 1", calls, pending)
	}
	if n := len(body.Trend.Buckets); n < 24 || n > 25 || body.Trend.BucketSeconds != 3600 {
		t.Fatalf("24h trend should be hourly: %d buckets of %ds", n, body.Trend.BucketSeconds)
	}
	if body.Trend.Buckets[0].Start != body.Trend.From || body.Trend.Buckets[len(body.Trend.Buckets)-1].End != body.Trend.To {
		t.Fatalf("trend should cover exactly the window: %#v", body.Trend)
	}
	for _, bucket := range body.Trend.Buckets[1:] {
		if bucket.Start%3600 != 0 {
			t.Fatalf("hourly buckets should start on the hour: %d", bucket.Start)
		}
	}

	unauth := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/trend", "", url.Values{"range": {"24h"}})
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("trend without a session should be 401, got %d", unauth.StatusCode)
	}
	bad := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/trend", cookie, url.Values{"range": {"90d"}})
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown trend range should be rejected, got %d", bad.StatusCode)
	}
}

func TestUserEventsBeforeCursorPagesBackwards(t *testing.T) {
	key := setupTestState(t)
	now := time.Now().Truncate(time.Second)
	for i, id := range []string{"req-1", "req-2", "req-3", "req-4", "req-5"} {
		insertPortalUsage(t, key, id, now.Add(-time.Duration(i+1)*10*time.Minute), 0.01, false)
	}
	cookie := plusSessionCookie(t, "sk-alice-secret")
	page := func(query url.Values) []string {
		t.Helper()
		resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/events", cookie, query)
		var body struct {
			Events []policyplus.UsageEvent `json:"events"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(body.Events))
		for _, event := range body.Events {
			ids = append(ids, event.RequestID)
		}
		return ids
	}
	first := page(url.Values{"range": {"24h"}, "limit": {"2"}})
	if strings.Join(first, ",") != "req-1,req-2" {
		t.Fatalf("first page = %v", first)
	}
	cursor := now.Add(-20 * time.Minute).Unix()
	older := page(url.Values{"range": {"24h"}, "limit": {"2"}, "before": {strconv.FormatInt(cursor, 10)}})
	if strings.Join(older, ",") != "req-2,req-3" {
		t.Fatalf("older page should restart at the cursor second (deduped by the page): %v", older)
	}
}

func TestUserMeReportsWhenExhaustedWindowsRecover(t *testing.T) {
	key := setupTestState(t)
	key.FiveHourUSD = floatPtr(1)
	key.DailyLimitUSD = floatPtr(50)
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldest := now.Add(-4 * time.Hour).Truncate(time.Second)
	insertPortalUsage(t, key, "req-oldest", oldest, 0.6, false)
	insertPortalUsage(t, key, "req-newer", now.Add(-time.Hour), 0.5, false)
	cookie := plusSessionCookie(t, "sk-alice-secret")

	resp := portalGet(t, "/v0/resource/plugins/cpa-key-policy-plus/user/api/me", cookie, nil)
	var body struct {
		Me struct {
			Quota map[string]map[string]any `json:"quota"`
		} `json:"me"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	fiveHour := body.Me.Quota[policyplus.Range5H]
	wantRecovery := oldest.Add(5*time.Hour + time.Second).Unix()
	if got, _ := fiveHour["recovers_at"].(float64); int64(got) != wantRecovery {
		t.Fatalf("5h recovers_at = %v, want %d (%#v)", fiveHour["recovers_at"], wantRecovery, fiveHour)
	}
	if _, ok := body.Me.Quota[policyplus.Range24H]["recovers_at"]; ok {
		t.Fatalf("a window under its limit should not report recovery: %#v", body.Me.Quota[policyplus.Range24H])
	}
	wantReset := policyplus.MonthResetAt(time.Now()).Unix()
	if got, _ := body.Me.Quota[policyplus.RangeMonth]["resets_at"].(float64); int64(got) != wantReset {
		t.Fatalf("month resets_at = %v, want %d", body.Me.Quota[policyplus.RangeMonth]["resets_at"], wantReset)
	}
}

func TestUserHTMLKeepsSessionAndLogsOutForReal(t *testing.T) {
	html := userHTML()
	for _, want := range []string{
		`api("/me"`,
		`api("/logout"`,
		`api(` + "`" + `/trend?range=${encodeURIComponent(range)}`,
		"&before=",
		"只看失败",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("user page missing %q", want)
		}
	}
	if strings.Contains(html, "location.reload()") {
		t.Fatal("logout must clear the server session instead of reloading into a still-valid cookie")
	}
}

func TestAdminHTMLExposesBillingResolveAndKeySearch(t *testing.T) {
	html := adminHTML()
	for _, want := range []string{"/keys/billing/resolve", "重新结算", `id="keySearch"`, "需要处理", "仅周额度", "未保存"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin page missing %q", want)
		}
	}
}

func TestUserSessionExplainsKeysMissingFromCPA(t *testing.T) {
	setupTestState(t)
	raw, err := userSession(managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"sk-not-in-cpa-config-000000000000"}}})
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized || body["error"] != "key_not_found" || strings.Contains(fmt2(body["message"]), "策略") {
		t.Fatalf("unknown key login = %d %v", resp.StatusCode, body)
	}
	if cookies := resp.Headers.Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("unknown key must not get a session: %v", cookies)
	}
	// A configured key still logs in.
	if cookie := plusSessionCookie(t, "sk-alice-secret"); cookie == "" {
		t.Fatal("configured key lost its session")
	}
}

func fmt2(v any) string { s, _ := v.(string); return s }
