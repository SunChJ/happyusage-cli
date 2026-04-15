package happyusage

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var Version = "dev"

//go:embed scripts/check-usage-agent.sh
var embeddedAgentScript string

type quota struct {
	Name         string   `json:"name"`
	Period       string   `json:"period,omitempty"`
	UsedPct      *float64 `json:"used_pct,omitempty"`
	LeftPct      *float64 `json:"left_pct,omitempty"`
	ResetsAt     string   `json:"resets_at,omitempty"`
	Remaining    *float64 `json:"remaining,omitempty"`
	Total        *float64 `json:"total,omitempty"`
	UsedDollars  *float64 `json:"used_dollars,omitempty"`
	LimitDollars *float64 `json:"limit_dollars,omitempty"`
}

type providerUsage struct {
	Provider          string         `json:"provider"`
	OK                bool           `json:"ok"`
	Error             string         `json:"error,omitempty"`
	CheckedAt         string         `json:"checked_at,omitempty"`
	Plan              string         `json:"plan,omitempty"`
	Quotas            []quota        `json:"quotas,omitempty"`
	Credits           map[string]any `json:"credits,omitempty"`
	ExtraUsage        map[string]any `json:"extra_usage,omitempty"`
	ExtraUsageBalance map[string]any `json:"extra_usage_balance,omitempty"`
	Raw               map[string]any `json:"-"`
}

type jsonEnvelope struct {
	OK            bool            `json:"ok"`
	Source        string          `json:"source"`
	CheckedAt     string          `json:"checked_at"`
	ProviderCount int             `json:"provider_count,omitempty"`
	Providers     []providerUsage `json:"providers,omitempty"`
	Provider      *providerUsage  `json:"provider,omitempty"`
	Error         string          `json:"error,omitempty"`
}

type app struct {
	progName string
	stdout   io.Writer
	stderr   io.Writer
}

type spinner struct {
	w      io.Writer
	stop   chan struct{}
	done   chan struct{}
}

func newSpinner(w io.Writer) *spinner {
	s := &spinner{w: w, stop: make(chan struct{}), done: make(chan struct{})}
	go s.run()
	return s
}

func (s *spinner) run() {
	defer close(s.done)
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			fmt.Fprintf(s.w, "\r\033[K")
			return
		case <-ticker.C:
			fmt.Fprintf(s.w, "\r%s Fetching usage...", frames[i%len(frames)])
			i++
		}
	}
}

func (s *spinner) Stop() {
	close(s.stop)
	<-s.done
}

type usageOptions struct {
	JSON   bool
	Agent  bool
	Target string
	Action string
}

type codexAuthFile struct {
	OpenAIAPIKey string `json:"OPENAI_API_KEY,omitempty"`
	AuthMode     string `json:"auth_mode,omitempty"`
	Tokens       struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id,omitempty"`
		IDToken      string `json:"id_token,omitempty"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh,omitempty"`
}

var (
	collectUsageFn       = collectUsageHybrid
	claudeUsageURL       = "https://api.anthropic.com/api/oauth/usage"
	claudeKeychainSvc    = "Claude Code-credentials"
	claudeBetaHeader     = "oauth-2025-04-20"
	cursorTokenURL       = "https://api2.cursor.sh/oauth/token"
	cursorClientID       = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
	cursorUsageURL       = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	cursorPlanURL        = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetPlanInfo"
	cursorCreditsURL     = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCreditGrantsBalance"
	windsurfStatusURL    = "https://server.self-serve.windsurf.com/exa.seat_management_pb.SeatManagementService/GetUserStatus"
	geminiLoadURL        = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiQuotaURL       = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	geminiTokenURL       = "https://oauth2.googleapis.com/token"
	codexUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	codexTokenURL        = "https://auth.openai.com/oauth/token"
	codexOAuthClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	httpClient           = &http.Client{Timeout: 15 * time.Second}
	allProviders         = []string{"claude", "codex", "cursor", "copilot", "gemini", "windsurf"}
)

func Main(progName string, args []string) int {
	return MainWithIO(progName, args, os.Stdout, os.Stderr)
}

func MainWithIO(progName string, args []string, stdout, stderr io.Writer) int {
	a := app{progName: progName, stdout: stdout, stderr: stderr}
	return a.run(args)
}

func (a app) run(args []string) int {
	if len(args) == 0 {
		a.printHelp()
		return 0
	}

	// Start async update check for human-facing commands
	var updateCh <-chan string
	if !isQuietMode(args) {
		updateCh = checkUpdateAsync()
	}

	var exitCode int
	switch args[0] {
	case "help", "-h", "--help":
		if len(args) > 1 && args[1] == "usage" {
			a.printUsageHelp()
		} else {
			a.printHelp()
		}
	case "version", "--version", "-v":
		fmt.Fprintln(a.stdout, Version)
	case "usage":
		opts, err := parseUsageArgs(args[1:])
		if err != nil {
			return a.exitErr(err)
		}
		exitCode = a.runUsage(opts)
	case "update":
		return a.runUpdate()
	default:
		return a.exitErr(fmt.Errorf("unknown command: %s", args[0]))
	}

	// Show update hint if available
	if updateCh != nil {
		if latest := <-updateCh; latest != "" {
			fmt.Fprintf(a.stderr, "\n💡 A new version is available: %s (current: %s)\n", latest, Version)
			fmt.Fprintf(a.stderr, "   Run 'hu update' to upgrade.\n")
		}
	}
	return exitCode
}

func isQuietMode(args []string) bool {
	for _, arg := range args {
		if arg == "--agent" || arg == "--json" || arg == "update" {
			return true
		}
	}
	return false
}

func parseUsageArgs(args []string) (usageOptions, error) {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "emit JSON envelope")
	agentOut := fs.Bool("agent", false, "emit compact agent-friendly text")

	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else {
			positionals = append(positionals, arg)
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		return usageOptions{}, err
	}

	opts := usageOptions{JSON: *jsonOut, Agent: *agentOut, Action: "all"}
	if len(positionals) > 0 {
		if positionals[0] == "list" {
			opts.Action = "list"
		} else {
			opts.Action = "provider"
			opts.Target = positionals[0]
		}
	}
	return opts, nil
}

func (a app) showSpinner(opts usageOptions) func() {
	if opts.Agent || opts.JSON {
		return func() {}
	}
	s := newSpinner(a.stderr)
	return s.Stop
}

func (a app) runUsage(opts usageOptions) int {
	now := time.Now().UTC().Format(time.RFC3339)

	switch opts.Action {
	case "provider":
		stop := a.showSpinner(opts)
		results, err := collectUsageFn([]string{opts.Target})
		stop()
		if err != nil {
			return a.renderUsageError(opts, err)
		}
		if len(results) == 0 {
			return a.renderUsageError(opts, fmt.Errorf("provider not found: %s", opts.Target))
		}
		provider := results[0]
		if opts.JSON {
			a.writeJSON(jsonEnvelope{OK: provider.OK, Source: "native_provider_scripts", CheckedAt: now, Provider: &provider, Error: provider.Error})
			if provider.OK {
				return 0
			}
			return 1
		}
		if !provider.OK {
			return a.exitErr(fmt.Errorf("%s: %s", provider.Provider, provider.Error))
		}
		if opts.Agent {
			a.printAgentProvider(provider)
			return 0
		}
		a.printHumanProviders([]providerUsage{provider})
		return 0
	case "list":
		stop := a.showSpinner(opts)
		results, err := collectUsageFn(nil)
		stop()
		if err != nil {
			return a.renderUsageError(opts, err)
		}
		configured := configuredProviders(results)
		if opts.JSON {
			a.writeJSON(jsonEnvelope{OK: true, Source: "native_provider_scripts", CheckedAt: now, ProviderCount: len(configured), Providers: configured})
			return 0
		}
		for _, provider := range configured {
			fmt.Fprintln(a.stdout, provider.Provider)
		}
		return 0
	default:
		stop := a.showSpinner(opts)
		results, err := collectUsageFn(nil)
		stop()
		if err != nil {
			return a.renderUsageError(opts, err)
		}
		configured := configuredProviders(results)
		if opts.JSON {
			a.writeJSON(jsonEnvelope{OK: true, Source: "native_provider_scripts", CheckedAt: now, ProviderCount: len(configured), Providers: configured})
			return 0
		}
		if opts.Agent {
			for _, provider := range configured {
				a.printAgentProvider(provider)
			}
			return 0
		}
		a.printHumanProviders(configured)
		return 0
	}
}

func configuredProviders(results []providerUsage) []providerUsage {
	configured := make([]providerUsage, 0, len(results))
	for _, result := range results {
		if result.OK {
			configured = append(configured, result)
		}
	}
	sort.Slice(configured, func(i, j int) bool { return configured[i].Provider < configured[j].Provider })
	return configured
}

func (a app) renderUsageError(opts usageOptions, err error) int {
	if opts.JSON {
		a.writeJSON(jsonEnvelope{OK: false, Source: "native_provider_scripts", CheckedAt: time.Now().UTC().Format(time.RFC3339), Error: err.Error()})
		return 1
	}
	return a.exitErr(err)
}

func collectUsageHybrid(targets []string) ([]providerUsage, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("native provider scripts currently support macOS only")
	}

	requested := normalizeTargets(targets)
	results := make([]providerUsage, 0, len(requested))
	scriptTargets := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))

	for _, target := range requested {
		if seen[target] {
			continue
		}
		seen[target] = true
		switch target {
		case "claude":
			result, err := collectClaudeUsage()
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case "codex":
			result, err := collectCodexUsage()
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case "copilot":
			result, err := collectCopilotUsage()
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case "gemini":
			result, err := collectGeminiUsage()
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case "cursor":
			result, err := collectCursorUsage()
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case "windsurf":
			result, err := collectWindsurfUsage()
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		default:
			scriptTargets = append(scriptTargets, target)
		}
	}

	if len(scriptTargets) > 0 {
		scriptResults, err := collectUsageViaScript(scriptTargets)
		if err != nil {
			return nil, err
		}
		results = append(results, scriptResults...)
	}

	return results, nil
}

func normalizeTargets(targets []string) []string {
	if len(targets) == 0 {
		return append([]string(nil), allProviders...)
	}
	return targets
}

func collectUsageViaScript(targets []string) ([]providerUsage, error) {
	scriptPath, cleanup, err := materializeScript()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	args := []string{scriptPath, "--raw"}
	args = append(args, targets...)
	cmd := exec.Command("bash", args...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("script execution failed: %v: %s", err, strings.TrimSpace(string(output)))
	}

	payload := extractJSONArray(output)
	var raw []map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse script output: %w", err)
	}
	results := make([]providerUsage, 0, len(raw))
	for _, item := range raw {
		results = append(results, decodeProviderUsage(item))
	}
	return results, nil
}

func materializeScript() (string, func(), error) {
	dir, err := os.MkdirTemp("", "happyusage-*")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "check-usage-agent.sh")
	if err := os.WriteFile(path, []byte(embeddedAgentScript), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	return path, cleanup, nil
}

func extractJSONArray(output []byte) []byte {
	text := strings.TrimSpace(string(output))
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end >= start {
		return []byte(text[start : end+1])
	}
	return output
}

func decodeProviderUsage(item map[string]any) providerUsage {
	result := providerUsage{Raw: item}
	if v, ok := item["provider"].(string); ok {
		result.Provider = v
	}
	if v, ok := item["ok"].(bool); ok {
		result.OK = v
	}
	if v, ok := item["error"].(string); ok {
		result.Error = v
	}
	if v, ok := item["checked_at"].(string); ok {
		result.CheckedAt = v
	}
	if v, ok := item["plan"].(string); ok {
		result.Plan = v
	}
	if v, ok := item["credits"].(map[string]any); ok {
		result.Credits = v
	}
	if v, ok := item["extra_usage"].(map[string]any); ok {
		result.ExtraUsage = v
	}
	if v, ok := item["extra_usage_balance"].(map[string]any); ok {
		result.ExtraUsageBalance = v
	}
	if arr, ok := item["quotas"].([]any); ok {
		for _, elem := range arr {
			qMap, ok := elem.(map[string]any)
			if !ok {
				continue
			}
			result.Quotas = append(result.Quotas, decodeQuota(qMap))
		}
	}
	return result
}

func decodeQuota(item map[string]any) quota {
	q := quota{}
	if v, ok := item["name"].(string); ok {
		q.Name = v
	}
	if v, ok := item["period"].(string); ok {
		q.Period = v
	}
	q.UsedPct = readNumberPtr(item["used_pct"])
	q.LeftPct = readNumberPtr(item["left_pct"])
	q.Remaining = readNumberPtr(item["remaining"])
	q.Total = readNumberPtr(item["total"])
	q.UsedDollars = readNumberPtr(item["used_dollars"])
	q.LimitDollars = readNumberPtr(item["limit_dollars"])
	if v, ok := item["resets_at"].(string); ok {
		q.ResetsAt = v
	}
	return q
}

func readNumberPtr(v any) *float64 {
	switch n := v.(type) {
	case float64:
		value := n
		return &value
	case int:
		value := float64(n)
		return &value
	case string:
		return nil
	default:
		return nil
	}
}

func collectClaudeUsage() (providerUsage, error) {
	raw, err := readClaudeCredentials()
	if err != nil {
		return providerUsage{}, err
	}
	if raw == "" {
		return providerUsage{Provider: "claude", OK: false, Error: "no credentials found"}, nil
	}

	token := extractClaudeAccessToken(raw)
	if token == "" {
		return providerUsage{Provider: "claude", OK: false, Error: "failed to parse token"}, nil
	}

	body, err := doClaudeUsageRequest(token)
	if err != nil {
		return providerUsage{Provider: "claude", OK: false, Error: err.Error()}, nil
	}
	return decodeClaudeUsage(body), nil
}

func readClaudeCredentials() (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", claudeKeychainSvc, "-w")
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	credPath := filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	data, readErr := os.ReadFile(credPath)
	if readErr == nil {
		return string(data), nil
	}
	if errors.Is(readErr, os.ErrNotExist) {
		return "", nil
	}
	return "", readErr
}

func extractClaudeAccessToken(raw string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	oauth, _ := parsed["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		return ""
	}
	token, _ := oauth["accessToken"].(string)
	return token
}

func doClaudeUsageRequest(token string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", claudeBetaHeader)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("api failed, token may be expired")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}
	return raw, nil
}

func decodeClaudeUsage(raw map[string]any) providerUsage {
	result := providerUsage{
		Provider:  "claude",
		OK:        true,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, cfg := range []struct {
		Key    string
		Name   string
		Period string
	}{
		{Key: "five_hour", Name: "session", Period: "5h"},
		{Key: "seven_day", Name: "weekly", Period: "7d"},
		{Key: "seven_day_sonnet", Name: "sonnet_weekly", Period: "7d"},
		{Key: "seven_day_opus", Name: "opus_weekly", Period: "7d"},
	} {
		window := nestedMap(raw, cfg.Key)
		used, ok := numberValue(window["utilization"])
		if !ok {
			continue
		}
		left := math.Round((100-used)*10) / 10
		q := quota{Name: cfg.Name, Period: cfg.Period, UsedPct: &used, LeftPct: &left}
		if resetAt, _ := window["resets_at"].(string); resetAt != "" {
			q.ResetsAt = resetAt
		}
		result.Quotas = append(result.Quotas, q)
	}
	extra := nestedMap(raw, "extra_usage")
	if enabled, _ := extra["is_enabled"].(bool); enabled {
		result.ExtraUsage = map[string]any{
			"enabled":   true,
			"used_usd":  extra["used_credits"],
			"limit_usd": extra["monthly_limit"],
		}
	}
	return result
}

func collectWindsurfUsage() (providerUsage, error) {
	apiKey, variant, err := readWindsurfAPIKey()
	if err != nil {
		return providerUsage{}, err
	}
	if apiKey == "" {
		return providerUsage{Provider: "windsurf", OK: false, Error: "not installed or not logged in"}, nil
	}
	body, err := doWindsurfStatusRequest(apiKey, variant)
	if err != nil {
		return providerUsage{Provider: "windsurf", OK: false, Error: "api request failed"}, nil
	}
	return decodeWindsurfUsage(body), nil
}

func readWindsurfAPIKey() (string, string, error) {
	candidates := []struct {
		Variant string
		DBPath  string
	}{
		{Variant: "windsurf", DBPath: filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Windsurf", "User", "globalStorage", "state.vscdb")},
		{Variant: "windsurf-next", DBPath: filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Windsurf - Next", "User", "globalStorage", "state.vscdb")},
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate.DBPath); err != nil {
			continue
		}
		authJSON, err := sqliteValue(candidate.DBPath, "SELECT value FROM ItemTable WHERE key='windsurfAuthStatus' LIMIT 1")
		if err != nil || authJSON == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(authJSON), &raw); err != nil {
			continue
		}
		if key := stringField(raw, "apiKey"); key != "" {
			return key, candidate.Variant, nil
		}
	}
	return "", "", nil
}

func doWindsurfStatusRequest(apiKey, variant string) (map[string]any, error) {
	payload := map[string]any{
		"metadata": map[string]any{
			"apiKey":           apiKey,
			"ideName":          variant,
			"ideVersion":       "1.108.2",
			"extensionName":    variant,
			"extensionVersion": "1.108.2",
			"locale":           "en",
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, windsurfStatusURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeWindsurfUsage(raw map[string]any) providerUsage {
	userStatus := nestedMap(raw, "userStatus")
	planStatus := nestedMap(userStatus, "planStatus")
	planInfo := nestedMap(planStatus, "planInfo")
	result := providerUsage{Provider: "windsurf", OK: true, CheckedAt: time.Now().UTC().Format(time.RFC3339), Plan: strings.TrimSpace(stringField(planInfo, "planName"))}
	for _, cfg := range []struct {
		Remaining string
		Reset     string
		Name      string
		Period    string
	}{
		{Remaining: "dailyQuotaRemainingPercent", Reset: "dailyQuotaResetAtUnix", Name: "daily", Period: "1d"},
		{Remaining: "weeklyQuotaRemainingPercent", Reset: "weeklyQuotaResetAtUnix", Name: "weekly", Period: "7d"},
	} {
		rem, ok := numberValue(planStatus[cfg.Remaining])
		if !ok {
			continue
		}
		used := math.Round((100-rem)*10) / 10
		left := math.Round(rem*10) / 10
		q := quota{Name: cfg.Name, Period: cfg.Period, UsedPct: &used, LeftPct: &left}
		if resetUnix, ok := numberValue(planStatus[cfg.Reset]); ok && resetUnix > 0 {
			q.ResetsAt = time.Unix(int64(resetUnix), 0).UTC().Format(time.RFC3339)
		}
		result.Quotas = append(result.Quotas, q)
	}
	if overage, ok := numberValue(planStatus["overageBalanceMicros"]); ok && overage > 0 {
		result.ExtraUsageBalance = map[string]any{"balance_usd": math.Round((overage/1e6)*100) / 100}
	}
	if len(result.Quotas) == 0 {
		return providerUsage{Provider: "windsurf", OK: false, Error: "quota data unavailable"}
	}
	return result
}

func collectCursorUsage() (providerUsage, error) {
	dbPath := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return providerUsage{Provider: "cursor", OK: false, Error: "not installed"}, nil
		}
		return providerUsage{}, err
	}
	token, err := sqliteValue(dbPath, "SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken' LIMIT 1")
	if err != nil {
		return providerUsage{}, err
	}
	refreshToken, err := sqliteValue(dbPath, "SELECT value FROM ItemTable WHERE key='cursorAuth/refreshToken' LIMIT 1")
	if err != nil {
		return providerUsage{}, err
	}
	if token == "" && refreshToken == "" {
		return providerUsage{Provider: "cursor", OK: false, Error: "not logged in"}, nil
	}
	if cursorTokenNeedsRefresh(token) && refreshToken != "" {
		if newToken, err := refreshCursorToken(refreshToken); err == nil && newToken != "" {
			token = newToken
		}
	}
	if token == "" {
		return providerUsage{Provider: "cursor", OK: false, Error: "no valid token"}, nil
	}
	usage, err := doCursorPOST(cursorUsageURL, token)
	if err != nil {
		return providerUsage{Provider: "cursor", OK: false, Error: "parse failed"}, nil
	}
	plan, err := doCursorPOST(cursorPlanURL, token)
	if err != nil {
		return providerUsage{Provider: "cursor", OK: false, Error: "parse failed"}, nil
	}
	credits, err := doCursorPOST(cursorCreditsURL, token)
	if err != nil {
		return providerUsage{Provider: "cursor", OK: false, Error: "parse failed"}, nil
	}
	return decodeCursorUsage(usage, plan, credits), nil
}

func sqliteValue(dbPath, query string) (string, error) {
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func cursorTokenNeedsRefresh(token string) bool {
	payload := parseJWTPayload(token)
	exp, ok := numberValue(payload["exp"])
	if !ok || exp <= 0 {
		return token == ""
	}
	return int64(exp) < time.Now().Add(5*time.Minute).Unix()
}

func refreshCursorToken(refreshToken string) (string, error) {
	payload := map[string]any{"grant_type": "refresh_token", "client_id": cursorClientID, "refresh_token": refreshToken}
	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, cursorTokenURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	return stringField(raw, "access_token"), nil
}

func doCursorPOST(endpoint, token string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeCursorUsage(usage, planData, creditsData map[string]any) providerUsage {
	result := providerUsage{Provider: "cursor", OK: true, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if plan := stringField(nestedMap(planData, "planInfo"), "planName"); plan != "" {
		result.Plan = plan
	}
	pu := nestedMap(usage, "planUsage")
	if tp, ok := numberValue(pu["totalPercentUsed"]); ok {
		left := math.Round((100-tp)*10) / 10
		q := quota{Name: "total", Period: "monthly", UsedPct: &tp, LeftPct: &left}
		if cycleEnd, ok := numberValue(usage["billingCycleEnd"]); ok {
			q.ResetsAt = time.UnixMilli(int64(cycleEnd)).UTC().Format(time.RFC3339)
		}
		result.Quotas = append(result.Quotas, q)
	} else if limit, ok := numberValue(pu["limit"]); ok && limit > 0 {
		spend, _ := numberValue(pu["totalSpend"])
		usedPct := math.Round((spend/limit*100)*10) / 10
		leftPct := math.Round((100-usedPct)*10) / 10
		usedUSD := math.Round((spend/100)*100) / 100
		limitUSD := math.Round((limit/100)*100) / 100
		result.Quotas = append(result.Quotas, quota{Name: "total", Period: "monthly", UsedPct: &usedPct, LeftPct: &leftPct, UsedDollars: &usedUSD, LimitDollars: &limitUSD})
	}
	for _, cfg := range []struct{ Key, Name string }{{"autoPercentUsed", "auto"}, {"apiPercentUsed", "api"}} {
		if used, ok := numberValue(pu[cfg.Key]); ok {
			left := math.Round((100-used)*10) / 10
			result.Quotas = append(result.Quotas, quota{Name: cfg.Name, Period: "monthly", UsedPct: &used, LeftPct: &left})
		}
	}
	if has, _ := creditsData["hasCreditGrants"].(bool); has {
		totalC, tok := numberValue(creditsData["totalCents"])
		usedC, uok := numberValue(creditsData["usedCents"])
		if tok && uok && totalC > 0 {
			result.Credits = map[string]any{
				"total_usd": math.Round(totalC) / 100,
				"used_usd":  math.Round(usedC) / 100,
				"left_usd":  math.Round(totalC-usedC) / 100,
			}
		}
	}
	return result
}

func parseJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var raw map[string]any
	_ = json.Unmarshal(payload, &raw)
	return raw
}

func collectGeminiUsage() (providerUsage, error) {
	credsPath := filepath.Join(os.Getenv("HOME"), ".gemini", "oauth_creds.json")
	creds, err := readGeminiCreds(credsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return providerUsage{Provider: "gemini", OK: false, Error: "not logged in, run 'gemini auth login'"}, nil
		}
		return providerUsage{}, err
	}
	if creds["access_token"] == nil {
		return providerUsage{Provider: "gemini", OK: false, Error: "no access_token in creds"}, nil
	}

	token := stringField(creds, "access_token")
	refreshToken := stringField(creds, "refresh_token")
	if geminiTokenNeedsRefresh(creds) && refreshToken != "" {
		newToken, newExpiry, err := refreshGeminiToken(refreshToken)
		if err == nil && newToken != "" {
			token = newToken
			creds["access_token"] = newToken
			if newExpiry > 0 {
				creds["expiry_date"] = newExpiry
			}
			_ = writeJSONFile(credsPath, creds, 0o600)
		}
	}

	loadBody, err := doGeminiPOST(geminiLoadURL, token, map[string]any{
		"metadata": map[string]any{
			"ideType":     "IDE_UNSPECIFIED",
			"platform":    "PLATFORM_UNSPECIFIED",
			"pluginType":  "GEMINI",
			"duetProject": "default",
		},
	})
	if err != nil {
		return providerUsage{Provider: "gemini", OK: false, Error: "quota api failed"}, nil
	}
	projectID := findFirstStringByKeys(loadBody, []string{"cloudaicompanionProject"})
	quotaPayload := map[string]any{}
	if projectID != "" {
		quotaPayload["project"] = projectID
	}
	quotaBody, err := doGeminiPOST(geminiQuotaURL, token, quotaPayload)
	if err != nil {
		return providerUsage{Provider: "gemini", OK: false, Error: "quota api failed"}, nil
	}
	if nestedErrorCode(quotaBody) == 401 {
		return providerUsage{Provider: "gemini", OK: false, Error: "token expired, run 'gemini auth login'"}, nil
	}
	return decodeGeminiUsage(loadBody, quotaBody), nil
}

func readGeminiCreds(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds map[string]any
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return creds, nil
}

func geminiTokenNeedsRefresh(creds map[string]any) bool {
	expiry, ok := numberValue(creds["expiry_date"])
	if !ok || expiry <= 0 {
		return false
	}
	expMS := int64(expiry)
	if expiry < 1e12 {
		expMS = int64(expiry * 1000)
	}
	return expMS < time.Now().Add(5*time.Minute).UnixMilli()
}

func refreshGeminiToken(refreshToken string) (string, int64, error) {
	clientID, clientSecret := findGeminiOAuthClient()
	if clientID == "" || clientSecret == "" {
		return "", 0, errors.New("missing gemini oauth client")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")
	req, err := http.NewRequest(http.MethodPost, geminiTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("gemini token refresh failed with HTTP %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", 0, err
	}
	token := stringField(raw, "access_token")
	expiresIn, _ := numberValue(raw["expires_in"])
	var expiry int64
	if expiresIn > 0 {
		expiry = time.Now().UnixMilli() + int64(expiresIn*1000)
	}
	return token, expiry, nil
}

func findGeminiOAuthClient() (string, string) {
	searchDirs := []string{
		filepath.Join(os.Getenv("HOME"), ".bun", "install", "global", "node_modules", "@google", "gemini-cli-core", "dist", "src", "code_assist"),
		filepath.Join(os.Getenv("HOME"), ".bun", "install", "global", "node_modules", "@google", "gemini-cli", "node_modules", "@google", "gemini-cli-core", "dist", "src", "code_assist"),
		filepath.Join(os.Getenv("HOME"), ".npm-global", "lib", "node_modules", "@google", "gemini-cli-core", "dist", "src", "code_assist"),
		filepath.Join(os.Getenv("HOME"), ".npm-global", "lib", "node_modules", "@google", "gemini-cli", "node_modules", "@google", "gemini-cli-core", "dist", "src", "code_assist"),
		"/usr/local/lib/node_modules/@google/gemini-cli-core/dist/src/code_assist",
		"/usr/local/lib/node_modules/@google/gemini-cli/node_modules/@google/gemini-cli-core/dist/src/code_assist",
		"/opt/homebrew/opt/gemini-cli/libexec/lib/node_modules/@google/gemini-cli/bundle",
		"/usr/local/opt/gemini-cli/libexec/lib/node_modules/@google/gemini-cli/bundle",
	}
	for _, dir := range searchDirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		clientID, clientSecret := scanGeminiOAuthDir(dir)
		if clientID != "" && clientSecret != "" {
			return clientID, clientSecret
		}
	}
	return "", ""
}

func scanGeminiOAuthDir(dir string) (string, string) {
	var clientID, clientSecret string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		if !strings.Contains(text, "OAUTH_CLIENT_ID") {
			return nil
		}
		clientID = extractQuotedJSConst(text, "OAUTH_CLIENT_ID")
		clientSecret = extractQuotedJSConst(text, "OAUTH_CLIENT_SECRET")
		if clientID != "" && clientSecret != "" {
			return io.EOF
		}
		return nil
	})
	return clientID, clientSecret
}

func extractQuotedJSConst(text, key string) string {
	needle := key + ` = "`
	idx := strings.Index(text, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	end := strings.Index(text[start:], `"`)
	if end < 0 {
		return ""
	}
	return text[start : start+end]
}

func doGeminiPOST(endpoint, token string, payload map[string]any) (map[string]any, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func nestedErrorCode(raw map[string]any) int {
	errMap := nestedMap(raw, "error")
	code, _ := numberValue(errMap["code"])
	return int(code)
}

func decodeGeminiUsage(loadBody, quotaBody map[string]any) providerUsage {
	plan := map[string]string{"standard-tier": "Paid", "free-tier": "Free", "legacy-tier": "Legacy"}[findFirstStringByKeys(loadBody, []string{"tier", "userTier", "subscriptionTier"})]
	buckets := collectGeminiBuckets(quotaBody)
	result := providerUsage{Provider: "gemini", OK: true, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if plan != "" {
		result.Plan = plan
	}
	for _, cfg := range []struct {
		Name   string
		Filter string
	}{
		{Name: "pro", Filter: "pro"},
		{Name: "flash", Filter: "flash"},
	} {
		best, ok := selectGeminiBucket(buckets, cfg.Filter)
		if !ok {
			continue
		}
		used := math.Round((1-best.Remaining)*1000) / 10
		left := math.Round(best.Remaining*1000) / 10
		q := quota{Name: cfg.Name, Period: "rolling", UsedPct: &used, LeftPct: &left}
		if best.Reset != "" {
			q.ResetsAt = best.Reset
		}
		result.Quotas = append(result.Quotas, q)
	}
	return result
}

type geminiBucket struct {
	Model     string
	Remaining float64
	Reset     string
}

func collectGeminiBuckets(raw any) []geminiBucket {
	var buckets []geminiBucket
	var walk func(any)
	walk = func(v any) {
		switch obj := v.(type) {
		case map[string]any:
			if rem, ok := numberValue(obj["remainingFraction"]); ok {
				buckets = append(buckets, geminiBucket{Model: stringField(obj, "modelId"), Remaining: rem, Reset: stringField(obj, "resetTime")})
			}
			if model := stringField(obj, "model_id"); model != "" {
				if rem, ok := numberValue(obj["remainingFraction"]); ok {
					buckets = append(buckets, geminiBucket{Model: model, Remaining: rem, Reset: stringField(obj, "reset_time")})
				}
			}
			for _, child := range obj {
				walk(child)
			}
		case []any:
			for _, child := range obj {
				walk(child)
			}
		}
	}
	walk(raw)
	return buckets
}

func selectGeminiBucket(buckets []geminiBucket, needle string) (geminiBucket, bool) {
	var best geminiBucket
	found := false
	for _, bucket := range buckets {
		model := strings.ToLower(bucket.Model)
		if !strings.Contains(model, "gemini") || !strings.Contains(model, needle) {
			continue
		}
		if !found || bucket.Remaining < best.Remaining {
			best = bucket
			found = true
		}
	}
	return best, found
}

func findFirstStringByKeys(raw any, keys []string) string {
	switch obj := raw.(type) {
	case map[string]any:
		for _, key := range keys {
			if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		for _, child := range obj {
			if found := findFirstStringByKeys(child, keys); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range obj {
			if found := findFirstStringByKeys(child, keys); found != "" {
				return found
			}
		}
	}
	return ""
}

func writeJSONFile(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

func collectCopilotUsage() (providerUsage, error) {
	token, err := readCopilotToken()
	if err != nil {
		return providerUsage{}, err
	}
	if token == "" {
		return providerUsage{Provider: "copilot", OK: false, Error: "not logged in, run 'gh auth login'"}, nil
	}

	raw, err := doCopilotUsageRequest(token)
	if err != nil {
		return providerUsage{Provider: "copilot", OK: false, Error: err.Error()}, nil
	}
	return decodeCopilotUsage(raw), nil
}

func readCopilotToken() (string, error) {
	cmd := exec.Command("gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func doCopilotUsageRequest(token string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("X-Github-Api-Version", "2025-04-01")
	req.Header.Set("Editor-Version", "vscode/1.96.2")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("api failed, token may be expired")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}
	return raw, nil
}

func decodeCopilotUsage(raw map[string]any) providerUsage {
	result := providerUsage{
		Provider:  "copilot",
		OK:        true,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Plan:      stringField(raw, "copilot_plan"),
	}

	snaps := nestedMap(raw, "quota_snapshots")
	resetDate := stringField(raw, "quota_reset_date")
	for _, cfg := range []struct {
		Key  string
		Name string
	}{
		{Key: "premium_interactions", Name: "premium"},
		{Key: "chat", Name: "chat"},
	} {
		snap := nestedMap(snaps, cfg.Key)
		rem, ok := numberValue(snap["percent_remaining"])
		if !ok {
			continue
		}
		used := math.Round((100-rem)*10) / 10
		left := math.Round(rem*10) / 10
		q := quota{Name: cfg.Name, Period: "monthly", UsedPct: &used, LeftPct: &left}
		if resetDate != "" {
			q.ResetsAt = resetDate
		}
		result.Quotas = append(result.Quotas, q)
	}

	limited := nestedMap(raw, "limited_user_quotas")
	monthly := nestedMap(raw, "monthly_quotas")
	limitedReset := stringField(raw, "limited_user_reset_date")
	for _, cfg := range []struct {
		Key  string
		Name string
	}{
		{Key: "chat", Name: "chat"},
		{Key: "completions", Name: "completions"},
	} {
		remaining, rok := numberValue(limited[cfg.Key])
		total, tok := numberValue(monthly[cfg.Key])
		if !rok || !tok || total <= 0 {
			continue
		}
		usedPct := math.Round((1-remaining/total)*1000) / 10
		leftPct := math.Round((100-usedPct)*10) / 10
		q := quota{Name: cfg.Name, Period: "monthly", UsedPct: &usedPct, LeftPct: &leftPct, Remaining: &remaining, Total: &total}
		if limitedReset != "" {
			q.ResetsAt = limitedReset
		}
		result.Quotas = append(result.Quotas, q)
	}

	return result
}

func collectCodexUsage() (providerUsage, error) {
	authPath := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	if home := os.Getenv("CODEX_HOME"); home != "" {
		authPath = filepath.Join(home, "auth.json")
	}

	auth, err := readCodexAuthFile(authPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return providerUsage{Provider: "codex", OK: false, Error: "auth file not found"}, nil
		}
		return providerUsage{}, err
	}
	if auth.Tokens.AccessToken == "" && auth.Tokens.RefreshToken == "" {
		return providerUsage{Provider: "codex", OK: false, Error: "no token found"}, nil
	}

	usage, token, refreshToken, err := fetchCodexUsage(auth)
	if err != nil {
		return providerUsage{Provider: "codex", OK: false, Error: err.Error()}, nil
	}
	if refreshToken != "" && (token != auth.Tokens.AccessToken || refreshToken != auth.Tokens.RefreshToken) {
		auth.Tokens.AccessToken = token
		auth.Tokens.RefreshToken = refreshToken
		auth.Tokens.AccountID = codexAccountID(token)
		auth.LastRefresh = time.Now().UTC().Format(time.RFC3339)
		if err := writeCodexAuthFile(authPath, auth); err != nil {
			return providerUsage{}, err
		}
	}

	return decodeCodexUsage(usage), nil
}

func readCodexAuthFile(path string) (codexAuthFile, error) {
	var auth codexAuthFile
	data, err := os.ReadFile(path)
	if err != nil {
		return auth, err
	}
	err = json.Unmarshal(data, &auth)
	return auth, err
}

func writeCodexAuthFile(path string, auth codexAuthFile) error {
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func fetchCodexUsage(auth codexAuthFile) (map[string]any, string, string, error) {
	accessToken := auth.Tokens.AccessToken
	refreshToken := auth.Tokens.RefreshToken
	accountID := auth.Tokens.AccountID
	if accountID == "" {
		accountID = codexAccountID(accessToken)
	}

	body, status, err := doCodexUsageRequest(accessToken, accountID)
	if err != nil {
		return nil, "", "", err
	}
	if status == http.StatusUnauthorized && refreshToken != "" {
		newAccess, newRefresh, err := refreshCodexAccessToken(refreshToken)
		if err != nil {
			return nil, "", "", err
		}
		if newAccess == "" {
			return nil, "", "", errors.New("codex: token refresh returned empty access token")
		}
		accessToken = newAccess
		if newRefresh != "" {
			refreshToken = newRefresh
		}
		accountID = codexAccountID(accessToken)
		body, status, err = doCodexUsageRequest(accessToken, accountID)
		if err != nil {
			return nil, "", "", err
		}
	}
	if status != http.StatusOK {
		return nil, "", "", fmt.Errorf("api returned HTTP %d", status)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", "", fmt.Errorf("parse failed: %w", err)
	}
	return raw, accessToken, refreshToken, nil
}

func doCodexUsageRequest(accessToken, accountID string) ([]byte, int, error) {
	if accessToken == "" {
		return nil, 0, errors.New("no token found")
	}
	req, err := http.NewRequest(http.MethodGet, codexUsageURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

func refreshCodexAccessToken(refreshToken string) (string, string, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", codexOAuthClientID)
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequest(http.MethodPost, codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("token refresh failed with HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", err
	}
	return parsed.AccessToken, parsed.RefreshToken, nil
}

func codexAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ""
	}
	auth, _ := raw["https://api.openai.com/auth"].(map[string]any)
	if auth == nil {
		return ""
	}
	accountID, _ := auth["chatgpt_account_id"].(string)
	return accountID
}

func decodeCodexUsage(raw map[string]any) providerUsage {
	result := providerUsage{
		Provider:  "codex",
		OK:        true,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Plan:      stringField(raw, "plan_type"),
		Credits: map[string]any{
			"balance": numberString(nestedMap(raw, "credits")["balance"]),
		},
	}

	result.Quotas = append(result.Quotas, decodeCodexWindow("session", "5h", nestedMap(nestedMap(raw, "rate_limit"), "primary_window"))...)
	result.Quotas = append(result.Quotas, decodeCodexWindow("weekly", "7d", nestedMap(nestedMap(raw, "rate_limit"), "secondary_window"))...)

	for _, entry := range sliceMap(raw["additional_rate_limits"]) {
		name := stringField(entry, "limit_name")
		if name == "" {
			name = "model"
		}
		rateLimit := nestedMap(entry, "rate_limit")
		result.Quotas = append(result.Quotas, decodeCodexWindow(name, "5h", nestedMap(rateLimit, "primary_window"))...)
		result.Quotas = append(result.Quotas, decodeCodexWindow(name+"_weekly", "7d", nestedMap(rateLimit, "secondary_window"))...)
	}

	return result
}

func decodeCodexWindow(name, period string, window map[string]any) []quota {
	used, ok := numberValue(window["used_percent"])
	if !ok {
		return nil
	}
	left := math.Round((100-used)*10) / 10
	q := quota{Name: name, Period: period, UsedPct: &used, LeftPct: &left}
	if resetAt := codexResetAt(window["reset_at"]); resetAt != "" {
		q.ResetsAt = resetAt
	}
	return []quota{q}
}

func codexResetAt(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t <= 0 {
			return ""
		}
		return time.Unix(int64(t), 0).UTC().Format(time.RFC3339)
	default:
		return ""
	}
}

func nestedMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	child, _ := m[key].(map[string]any)
	return child
}

func sliceMap(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, _ := m[key].(string)
	return value
}

func numberValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		value, err := n.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}

func numberString(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		if n == math.Trunc(n) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int:
		return strconv.Itoa(n)
	case json.Number:
		return n.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (a app) printHelp() {
	fmt.Fprintf(a.stdout, "hu (happyusage) — check your AI provider usage, worry less\n\n")
	fmt.Fprintf(a.stdout, "Usage:\n")
	fmt.Fprintf(a.stdout, "  %s usage [provider] [--agent|--json]\n\n", a.progName)
	fmt.Fprintf(a.stdout, "Examples:\n")
	fmt.Fprintf(a.stdout, "  %s usage                        show all providers\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage claude                  show a single provider\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage list                    list available provider IDs\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage --agent                 compact text for AI agents\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage --json                  structured JSON for web UI\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage claude --agent          single provider, agent format\n\n", a.progName)
	fmt.Fprintf(a.stdout, "Other:\n")
	fmt.Fprintf(a.stdout, "  %s update                        update to latest version\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s help [command]                show help\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s version                       show version\n\n", a.progName)
	fmt.Fprintf(a.stdout, "Providers: claude, codex, cursor, copilot, gemini, windsurf\n")
}

func (a app) printUsageHelp() {
	fmt.Fprintf(a.stdout, "hu usage — check provider usage quotas and reset times\n\n")
	fmt.Fprintf(a.stdout, "Usage:\n")
	fmt.Fprintf(a.stdout, "  %s usage                        show all providers\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage <provider>              show a single provider\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage list                    list available provider IDs\n\n", a.progName)
	fmt.Fprintf(a.stdout, "Flags:\n")
	fmt.Fprintf(a.stdout, "  --agent    compact text for AI agents\n")
	fmt.Fprintf(a.stdout, "  --json     structured JSON for web UI\n")
}

func (a app) printHumanProviders(providers []providerUsage) {
	for i, provider := range providers {
		if i > 0 {
			fmt.Fprintln(a.stdout)
		}
		title := strings.Title(provider.Provider)
		fmt.Fprintf(a.stdout, "%s (%s)\n", title, provider.Provider)
		if provider.Plan != "" {
			fmt.Fprintf(a.stdout, "  plan      %s\n", provider.Plan)
		}
		for _, q := range provider.Quotas {
			fmt.Fprintf(a.stdout, "  %-9s %s %s left", q.Name, progressBar(q.LeftPct), percentString(q.LeftPct))
			if q.Remaining != nil && q.Total != nil {
				fmt.Fprintf(a.stdout, "  %.0f/%.0f", *q.Remaining, *q.Total)
			}
			if q.UsedDollars != nil && q.LimitDollars != nil {
				fmt.Fprintf(a.stdout, "  $%.2f/$%.2f", *q.UsedDollars, *q.LimitDollars)
			}
			if q.ResetsAt != "" {
				fmt.Fprintf(a.stdout, "  · %s", resetDisplay(q.ResetsAt))
			}
			fmt.Fprintln(a.stdout)
		}
		if provider.Credits != nil {
			printCreditsHuman(a.stdout, provider.Provider, provider.Credits)
		}
		if provider.ExtraUsage != nil {
			printMapHuman(a.stdout, "extra", provider.ExtraUsage)
		}
		if provider.ExtraUsageBalance != nil {
			printMapHuman(a.stdout, "extra", provider.ExtraUsageBalance)
		}
	}
}

func (a app) printAgentProvider(provider providerUsage) {
	parts := []string{provider.Provider}
	if provider.Plan != "" {
		parts = append(parts, "plan="+provider.Plan)
	}
	for _, q := range provider.Quotas {
		name := slug(q.Name)
		if q.LeftPct != nil {
			parts = append(parts, fmt.Sprintf("%s_left=%s", name, percentString(q.LeftPct)))
		}
		if q.ResetsAt != "" {
			if countdown, local := resetAgentFields(q.ResetsAt); countdown != "" {
				parts = append(parts, fmt.Sprintf("%s_reset_in=%s", name, countdown))
				parts = append(parts, fmt.Sprintf("%s_reset_local=%s", name, local))
			}
		}
	}
	if provider.Credits != nil {
		parts = append(parts, agentCredits(provider.Provider, provider.Credits)...)
	}
	fmt.Fprintln(a.stdout, strings.Join(parts, " | "))
}

func printCreditsHuman(w io.Writer, provider string, credits map[string]any) {
	switch provider {
	case "codex":
		fmt.Fprintf(w, "  credits   %v left\n", credits["balance"])
	case "cursor":
		left, lok := credits["left_usd"]
		total, tok := credits["total_usd"]
		used, uok := credits["used_usd"]
		if lok && tok && uok {
			fmt.Fprintf(w, "  credits   $%v left  ($%v used / $%v total)\n", left, used, total)
			return
		}
		fmt.Fprintf(w, "  credits   %v\n", credits)
	default:
		fmt.Fprintf(w, "  credits   %v\n", credits)
	}
}

func agentCredits(provider string, credits map[string]any) []string {
	switch provider {
	case "codex":
		return []string{fmt.Sprintf("credits_left=%v", credits["balance"])}
	case "cursor":
		return []string{fmt.Sprintf("credits_left_usd=%v", credits["left_usd"])}
	default:
		return []string{fmt.Sprintf("credits=%v", credits)}
	}
}

func printMapHuman(w io.Writer, label string, values map[string]any) {
	chunks := make([]string, 0, len(values))
	for k, v := range values {
		chunks = append(chunks, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(chunks)
	fmt.Fprintf(w, "  %-9s %s\n", label, strings.Join(chunks, " "))
}

func progressBar(percent *float64) string {
	const width = 16
	if percent == nil {
		return "[????????????????]"
	}
	p := math.Max(0, math.Min(100, *percent))
	filled := int(math.Round((p / 100) * width))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func percentString(percent *float64) string {
	if percent == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *percent)
}

func shortTime(value string) string {
	if t, ok := parseResetTime(value); ok {
		return t.Local().Format("2006-01-02 15:04")
	}
	return value
}

func parseResetTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	formats := []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02"}
	for _, format := range formats {
		if t, err := time.Parse(format, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func resetDisplay(value string) string {
	t, ok := parseResetTime(value)
	if !ok {
		return "reset " + value
	}
	countdown := humanDurationUntil(t)
	local := t.Local().Format("2006-01-02 15:04")
	return fmt.Sprintf("reset in %s (%s)", countdown, local)
}

func resetAgentFields(value string) (string, string) {
	t, ok := parseResetTime(value)
	if !ok {
		return "", ""
	}
	return agentDurationUntil(t), t.Local().Format("2006-01-02_15:04")
}

func humanDurationUntil(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "0m"
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours >= 24 {
		days := hours / 24
		hours = hours % 24
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func agentDurationUntil(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "0m"
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours >= 24 {
		days := hours / 24
		hours = hours % 24
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func (a app) writeJSON(v any) {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (a app) exitErr(err error) int {
	fmt.Fprintln(a.stderr, "error:", err)
	return 1
}

// --- update check ---

func cacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".happyusage")
}

func cacheFile() string {
	return filepath.Join(cacheDir(), "latest-version")
}

type versionCache struct {
	Version   string `json:"version"`
	CheckedAt int64  `json:"checked_at"`
}

func readCache() (string, bool) {
	data, err := os.ReadFile(cacheFile())
	if err != nil {
		return "", false
	}
	var c versionCache
	if err := json.Unmarshal(data, &c); err != nil {
		return "", false
	}
	if time.Now().Unix()-c.CheckedAt > 86400 {
		return "", false
	}
	return c.Version, true
}

func writeCache(version string) {
	_ = os.MkdirAll(cacheDir(), 0o755)
	data, _ := json.Marshal(versionCache{Version: version, CheckedAt: time.Now().Unix()})
	_ = os.WriteFile(cacheFile(), data, 0o644)
}

func fetchLatestVersion() string {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/SunChJ/happyusage/releases/latest", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return ""
	}
	return release.TagName
}

func checkUpdateAsync() <-chan string {
	ch := make(chan string, 1)
	if Version == "dev" {
		ch <- ""
		return ch
	}
	go func() {
		latest, ok := readCache()
		if !ok {
			latest = fetchLatestVersion()
			if latest != "" {
				writeCache(latest)
			}
		}
		if isNewerVersion(latest, Version) {
			ch <- latest
		} else {
			ch <- ""
		}
	}()
	return ch
}

func isNewerVersion(latest, current string) bool {
	if latest == "" || current == "" || latest == current {
		return false
	}

	latestParts, latestOK := parseSemver(latest)
	currentParts, currentOK := parseSemver(current)
	if latestOK && currentOK {
		for i := range latestParts {
			if latestParts[i] > currentParts[i] {
				return true
			}
			if latestParts[i] < currentParts[i] {
				return false
			}
		}
		return false
	}

	return latest != current
}

func parseSemver(v string) ([3]int, bool) {
	var parts [3]int
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	chunks := strings.Split(v, ".")
	if len(chunks) != 3 {
		return parts, false
	}
	for i, chunk := range chunks {
		n, err := strconv.Atoi(chunk)
		if err != nil {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// --- hu update ---

func (a app) runUpdate() int {
	latest := fetchLatestVersion()
	if latest == "" {
		return a.exitErr(fmt.Errorf("failed to check for updates"))
	}
	if latest == Version {
		fmt.Fprintf(a.stdout, "Already up to date (%s).\n", Version)
		return 0
	}

	fmt.Fprintf(a.stdout, "Updating %s → %s\n", Version, latest)

	// Detect install method and update
	if path, err := exec.LookPath("hu"); err == nil {
		if strings.Contains(path, "homebrew") || strings.Contains(path, "Cellar") {
			return a.updateViaBrew()
		}
	}
	if _, err := exec.LookPath("go"); err == nil {
		if gopath := os.Getenv("GOPATH"); gopath != "" {
			if exePath, _ := os.Executable(); strings.HasPrefix(exePath, gopath) {
				return a.updateViaGo()
			}
		}
		if home, _ := os.UserHomeDir(); home != "" {
			if exePath, _ := os.Executable(); strings.HasPrefix(exePath, filepath.Join(home, "go")) {
				return a.updateViaGo()
			}
		}
	}
	return a.updateViaScript()
}

func (a app) updateViaBrew() int {
	fmt.Fprintln(a.stdout, "Updating via Homebrew...")
	cmd := exec.Command("brew", "update")
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	_ = cmd.Run()
	cmd = exec.Command("brew", "upgrade", "hu")
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		return a.exitErr(fmt.Errorf("brew upgrade failed: %w", err))
	}
	fmt.Fprintln(a.stdout, "Done.")
	return 0
}

func (a app) updateViaGo() int {
	fmt.Fprintln(a.stdout, "Updating via go install...")
	cmd := exec.Command("go", "install", "github.com/SunChJ/happyusage/cmd/hu@latest")
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		return a.exitErr(fmt.Errorf("go install failed: %w", err))
	}
	fmt.Fprintln(a.stdout, "Done.")
	return 0
}

func (a app) updateViaScript() int {
	fmt.Fprintln(a.stdout, "Updating via install script...")
	cmd := exec.Command("bash", "-c", "curl -fsSL https://raw.githubusercontent.com/SunChJ/happyusage/main/scripts/install.sh | sh")
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		return a.exitErr(fmt.Errorf("script update failed: %w", err))
	}
	fmt.Fprintln(a.stdout, "Done.")
	return 0
}
