package happyusage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withMockCollector(results []providerUsage, err error, fn func()) {
	old := collectUsageFn
	collectUsageFn = func(targets []string) ([]providerUsage, error) {
		return results, err
	}
	defer func() { collectUsageFn = old }()
	fn()
}

func TestMainWithNoArgsShowsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := MainWithIO("hu", nil, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("unexpected help output: %s", stdout.String())
	}
}

func TestUsageListShowsConfiguredProviderIDs(t *testing.T) {
	results := []providerUsage{{Provider: "claude", OK: true}, {Provider: "codex", OK: true}, {Provider: "cursor", OK: false, Error: "not logged in"}}
	withMockCollector(results, nil, func() {
		var stdout, stderr bytes.Buffer
		exitCode := MainWithIO("hu", []string{"usage", "list"}, &stdout, &stderr)
		if exitCode != 0 {
			t.Fatalf("expected exit 0, got %d, stderr=%s", exitCode, stderr.String())
		}
		got := stdout.String()
		if !strings.Contains(got, "claude") || !strings.Contains(got, "codex") || strings.Contains(got, "cursor") {
			t.Fatalf("unexpected list output: %s", got)
		}
	})
}

func TestUsageProviderAgentOutput(t *testing.T) {
	results := []providerUsage{{Provider: "claude", OK: true, Plan: "Pro", Quotas: []quota{{Name: "session", LeftPct: numPtr(75), ResetsAt: "2099-01-01T12:00:00Z"}, {Name: "weekly", LeftPct: numPtr(60)}}}}
	withMockCollector(results, nil, func() {
		var stdout, stderr bytes.Buffer
		exitCode := MainWithIO("hu", []string{"usage", "claude", "--agent"}, &stdout, &stderr)
		if exitCode != 0 {
			t.Fatalf("expected exit 0, got %d, stderr=%s", exitCode, stderr.String())
		}
		got := stdout.String()
		if !strings.Contains(got, "claude") || !strings.Contains(got, "session_left=75.0%") || !strings.Contains(got, "session_reset_in=") || !strings.Contains(got, "session_reset_local=") {
			t.Fatalf("unexpected agent output: %s", got)
		}
	})
}

func TestUsageProviderJSONOutput(t *testing.T) {
	results := []providerUsage{{Provider: "claude", OK: true, Plan: "Pro"}}
	withMockCollector(results, nil, func() {
		var stdout, stderr bytes.Buffer
		exitCode := MainWithIO("hu", []string{"usage", "claude", "--json"}, &stdout, &stderr)
		if exitCode != 0 {
			t.Fatalf("expected exit 0, got %d, stderr=%s", exitCode, stderr.String())
		}
		got := stdout.String()
		if !strings.Contains(got, "\"provider\": \"claude\"") || !strings.Contains(got, "\"source\": \"native_provider_scripts\"") {
			t.Fatalf("unexpected json output: %s", got)
		}
	})
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		{name: "newer patch", latest: "v0.2.1", current: "v0.2.0", want: true},
		{name: "older cached version", latest: "v0.2.0", current: "v0.2.1", want: false},
		{name: "same version", latest: "v0.2.1", current: "v0.2.1", want: false},
		{name: "newer minor", latest: "v0.3.0", current: "v0.2.9", want: true},
		{name: "non semver fallback equal", latest: "main", current: "main", want: false},
		{name: "non semver fallback different", latest: "nightly", current: "main", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNewerVersion(tt.latest, tt.current); got != tt.want {
				t.Fatalf("isNewerVersion(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func TestExtractClaudeAccessToken(t *testing.T) {
	raw := `{"claudeAiOauth":{"accessToken":"claude-token"}}`
	if got := extractClaudeAccessToken(raw); got != "claude-token" {
		t.Fatalf("unexpected token: %q", got)
	}
}

func TestDecodeClaudeUsage(t *testing.T) {
	raw := map[string]any{
		"five_hour": map[string]any{"utilization": float64(33), "resets_at": "2030-01-01T00:00:00Z"},
		"seven_day": map[string]any{"utilization": float64(55), "resets_at": "2030-01-02T00:00:00Z"},
		"extra_usage": map[string]any{"is_enabled": true, "used_credits": float64(12.5), "monthly_limit": float64(50)},
	}
	got := decodeClaudeUsage(raw)
	if !got.OK || got.Provider != "claude" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Quotas) != 2 {
		t.Fatalf("expected 2 quotas, got %+v", got.Quotas)
	}
	if got.Quotas[0].Name != "session" || got.Quotas[0].LeftPct == nil || *got.Quotas[0].LeftPct != 67 {
		t.Fatalf("unexpected session quota: %+v", got.Quotas[0])
	}
	if got.ExtraUsage == nil || got.ExtraUsage["enabled"] != true {
		t.Fatalf("unexpected extra usage: %+v", got.ExtraUsage)
	}
}

func TestDecodeCursorUsage(t *testing.T) {
	usage := map[string]any{
		"billingCycleEnd": float64(1893456000000),
		"planUsage": map[string]any{
			"totalPercentUsed": float64(35),
			"autoPercentUsed":  float64(15),
			"apiPercentUsed":   float64(45),
		},
	}
	plan := map[string]any{"planInfo": map[string]any{"planName": "Pro"}}
	credits := map[string]any{"hasCreditGrants": true, "totalCents": float64(2000), "usedCents": float64(500)}
	got := decodeCursorUsage(usage, plan, credits)
	if !got.OK || got.Provider != "cursor" || got.Plan != "Pro" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Quotas) != 3 {
		t.Fatalf("expected 3 quotas, got %+v", got.Quotas)
	}
	if got.Credits == nil || got.Credits["left_usd"] != 15.0 {
		t.Fatalf("unexpected credits: %+v", got.Credits)
	}
}

func TestDecodeGeminiUsage(t *testing.T) {
	loadBody := map[string]any{"subscriptionTier": "standard-tier", "nested": map[string]any{"cloudaicompanionProject": "p1"}}
	quotaBody := map[string]any{
		"buckets": []any{
			map[string]any{"modelId": "gemini-pro", "remainingFraction": float64(0.4), "resetTime": "2030-01-01T00:00:00Z"},
			map[string]any{"modelId": "gemini-2.0-flash", "remainingFraction": float64(0.7), "resetTime": "2030-01-02T00:00:00Z"},
			map[string]any{"modelId": "gemini-pro", "remainingFraction": float64(0.2), "resetTime": "2030-01-03T00:00:00Z"},
		},
	}
	got := decodeGeminiUsage(loadBody, quotaBody)
	if !got.OK || got.Provider != "gemini" || got.Plan != "Paid" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Quotas) != 2 {
		t.Fatalf("expected 2 quotas, got %+v", got.Quotas)
	}
	if got.Quotas[0].Name != "pro" || got.Quotas[0].LeftPct == nil || *got.Quotas[0].LeftPct != 20 {
		t.Fatalf("unexpected pro quota: %+v", got.Quotas[0])
	}
}

func TestGeminiTokenNeedsRefresh(t *testing.T) {
	soon := float64(time.Now().Add(2 * time.Minute).UnixMilli())
	later := float64(time.Now().Add(10 * time.Minute).UnixMilli())
	if !geminiTokenNeedsRefresh(map[string]any{"expiry_date": soon}) {
		t.Fatal("expected near expiry token to need refresh")
	}
	if geminiTokenNeedsRefresh(map[string]any{"expiry_date": later}) {
		t.Fatal("expected later expiry token to skip refresh")
	}
}

func TestDecodeCopilotUsage(t *testing.T) {
	raw := map[string]any{
		"copilot_plan": "business",
		"quota_reset_date": "2030-02-01",
		"limited_user_reset_date": "2030-02-01",
		"quota_snapshots": map[string]any{
			"premium_interactions": map[string]any{"percent_remaining": float64(72)},
			"chat": map[string]any{"percent_remaining": float64(25)},
		},
		"limited_user_quotas": map[string]any{"chat": float64(30), "completions": float64(10)},
		"monthly_quotas": map[string]any{"chat": float64(60), "completions": float64(40)},
	}
	got := decodeCopilotUsage(raw)
	if !got.OK || got.Provider != "copilot" || got.Plan != "business" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(got.Quotas) != 4 {
		t.Fatalf("expected 4 quotas, got %+v", got.Quotas)
	}
	if got.Quotas[0].Name != "premium" || got.Quotas[0].LeftPct == nil || *got.Quotas[0].LeftPct != 72 {
		t.Fatalf("unexpected premium quota: %+v", got.Quotas[0])
	}
	if got.Quotas[2].Name != "chat" || got.Quotas[2].Remaining == nil || *got.Quotas[2].Remaining != 30 {
		t.Fatalf("unexpected limited chat quota: %+v", got.Quotas[2])
	}
}

func TestDecodeCodexUsage(t *testing.T) {
	raw := map[string]any{
		"plan_type": "plus",
		"credits": map[string]any{"balance": float64(3)},
		"rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": float64(25), "reset_at": float64(1893456000)},
			"secondary_window": map[string]any{"used_percent": float64(55), "reset_at": "2030-01-01T00:00:00Z"},
		},
	}

	got := decodeCodexUsage(raw)
	if !got.OK || got.Provider != "codex" || got.Plan != "plus" {
		t.Fatalf("unexpected provider usage: %+v", got)
	}
	if got.Credits["balance"] != "3" {
		t.Fatalf("unexpected credits: %+v", got.Credits)
	}
	if len(got.Quotas) != 2 {
		t.Fatalf("expected 2 quotas, got %+v", got.Quotas)
	}
	if got.Quotas[0].Name != "session" || got.Quotas[0].LeftPct == nil || *got.Quotas[0].LeftPct != 75 {
		t.Fatalf("unexpected session quota: %+v", got.Quotas[0])
	}
	if got.Quotas[1].Name != "weekly" || got.Quotas[1].LeftPct == nil || *got.Quotas[1].LeftPct != 45 {
		t.Fatalf("unexpected weekly quota: %+v", got.Quotas[1])
	}
}

func TestCollectCodexUsageRefreshesToken(t *testing.T) {
	oldClient := httpClient
	oldUsageURL := codexUsageURL
	oldTokenURL := codexTokenURL
	oldHome := os.Getenv("CODEX_HOME")
	defer func() {
		httpClient = oldClient
		codexUsageURL = oldUsageURL
		codexTokenURL = oldTokenURL
		_ = os.Setenv("CODEX_HOME", oldHome)
	}()

	var usageCalls int
	refreshedAccessToken := jwtWithAccount("acct-123")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usage":
			usageCalls++
			if got := r.Header.Get("Authorization"); usageCalls == 1 && got != "Bearer old-token" {
				t.Fatalf("unexpected first auth header: %s", got)
			}
			if usageCalls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+refreshedAccessToken {
				t.Fatalf("unexpected second auth header: %s", got)
			}
			if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct-123" {
				t.Fatalf("unexpected account header: %s", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"plan_type": "plus",
				"credits": map[string]any{"balance": float64(0)},
				"rate_limit": map[string]any{
					"primary_window": map[string]any{"used_percent": float64(47), "reset_at": "2030-01-01T00:00:00Z"},
					"secondary_window": map[string]any{"used_percent": float64(58), "reset_at": "2030-01-02T00:00:00Z"},
				},
			})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("refresh_token") != "refresh-1" {
				t.Fatalf("unexpected refresh token: %s", r.Form.Get("refresh_token"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  jwtWithAccount("acct-123"),
				"refresh_token": "refresh-2",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	httpClient = server.Client()
	codexUsageURL = server.URL + "/usage"
	codexTokenURL = server.URL + "/token"

	tmp := t.TempDir()
	if err := os.Setenv("CODEX_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	auth := codexAuthFile{}
	auth.Tokens.AccessToken = "old-token"
	auth.Tokens.RefreshToken = "refresh-1"
	if err := writeCodexAuthFile(filepath.Join(tmp, "auth.json"), auth); err != nil {
		t.Fatal(err)
	}

	got, err := collectCodexUsage()
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Plan != "plus" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if usageCalls != 2 {
		t.Fatalf("expected 2 usage calls, got %d", usageCalls)
	}
	updated, err := readCodexAuthFile(filepath.Join(tmp, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Tokens.RefreshToken != "refresh-2" || updated.Tokens.AccessToken == "old-token" {
		t.Fatalf("auth file not refreshed: %+v", updated)
	}
}

func jwtWithAccount(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func numPtr(v float64) *float64 { return &v }
