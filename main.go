package happyusage

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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

type usageOptions struct {
	JSON   bool
	Agent  bool
	Target string
	Action string
}

var collectUsageFn = collectUsageViaScript

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

	switch args[0] {
	case "help", "-h", "--help":
		if len(args) > 1 && args[1] == "usage" {
			a.printUsageHelp()
			return 0
		}
		a.printHelp()
		return 0
	case "version", "--version", "-v":
		fmt.Fprintln(a.stdout, Version)
		return 0
	case "usage":
		opts, err := parseUsageArgs(args[1:])
		if err != nil {
			return a.exitErr(err)
		}
		return a.runUsage(opts)
	default:
		return a.exitErr(fmt.Errorf("unknown command: %s", args[0]))
	}
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

func (a app) runUsage(opts usageOptions) int {
	now := time.Now().UTC().Format(time.RFC3339)

	switch opts.Action {
	case "provider":
		results, err := collectUsageFn([]string{opts.Target})
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
		results, err := collectUsageFn(nil)
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
		results, err := collectUsageFn(nil)
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

func collectUsageViaScript(targets []string) ([]providerUsage, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("native provider scripts currently support macOS only")
	}
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
