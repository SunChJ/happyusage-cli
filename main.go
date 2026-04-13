package happyusage

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultBaseURL = "http://127.0.0.1:6736"

var Version = "dev"

type apiSnapshot struct {
	ProviderID  string       `json:"providerId"`
	DisplayName string       `json:"displayName"`
	Plan        string       `json:"plan"`
	Lines       []metricLine `json:"lines"`
	FetchedAt   string       `json:"fetchedAt"`
}

type metricLine struct {
	Type             string      `json:"type"`
	Label            string      `json:"label"`
	Used             float64     `json:"used,omitempty"`
	Limit            float64     `json:"limit,omitempty"`
	Format           *lineFormat `json:"format,omitempty"`
	ResetsAt         string      `json:"resetsAt,omitempty"`
	PeriodDurationMs int64       `json:"periodDurationMs,omitempty"`
	Color            string      `json:"color,omitempty"`
	Value            string      `json:"value,omitempty"`
	Text             string      `json:"text,omitempty"`
	Subtitle         string      `json:"subtitle,omitempty"`
}

type lineFormat struct {
	Kind   string `json:"kind"`
	Suffix string `json:"suffix,omitempty"`
}

type normalizedProgress struct {
	Label            string   `json:"label"`
	Used             float64  `json:"used"`
	Limit            float64  `json:"limit"`
	Remaining        *float64 `json:"remaining,omitempty"`
	PercentUsed      *float64 `json:"percent_used,omitempty"`
	Unit             string   `json:"unit,omitempty"`
	ResetsAt         string   `json:"resets_at,omitempty"`
	PeriodDurationMs int64    `json:"period_duration_ms,omitempty"`
}

type normalizedText struct {
	Label    string `json:"label"`
	Value    string `json:"value"`
	Subtitle string `json:"subtitle,omitempty"`
}

type normalizedBadge struct {
	Label    string `json:"label"`
	Text     string `json:"text"`
	Subtitle string `json:"subtitle,omitempty"`
}

type normalizedSnapshot struct {
	ProviderID      string               `json:"provider_id"`
	DisplayName     string               `json:"display_name"`
	Plan            string               `json:"plan,omitempty"`
	FetchedAt       string               `json:"fetched_at,omitempty"`
	Progress        []normalizedProgress `json:"progress,omitempty"`
	Texts           []normalizedText     `json:"texts,omitempty"`
	Badges          []normalizedBadge    `json:"badges,omitempty"`
	PrimaryProgress *normalizedProgress  `json:"primary_progress,omitempty"`
}

type jsonEnvelope struct {
	OK            bool                 `json:"ok"`
	Source        string               `json:"source"`
	BaseURL       string               `json:"base_url"`
	CheckedAt     string               `json:"checked_at"`
	ProviderCount int                  `json:"provider_count,omitempty"`
	Providers     []normalizedSnapshot `json:"providers,omitempty"`
	Provider      *normalizedSnapshot  `json:"provider,omitempty"`
	Error         string               `json:"error,omitempty"`
	Message       string               `json:"message,omitempty"`
}

type app struct {
	progName string
	stdout   io.Writer
	stderr   io.Writer
}

type usageOptions struct {
	BaseURL string
	Timeout time.Duration
	JSON    bool
	Agent   bool
	Target  string
	Action  string
}

func Main(progName string, args []string) int {
	client := &http.Client{Timeout: 5 * time.Second}
	return MainWithIO(progName, args, os.Stdout, os.Stderr, client)
}

func MainWithIO(progName string, args []string, stdout, stderr io.Writer, client *http.Client) int {
	a := app{progName: progName, stdout: stdout, stderr: stderr}
	return a.run(args, client)
}

func (a app) run(args []string, client *http.Client) int {
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
		client.Timeout = opts.Timeout
		return a.runUsage(client, opts)
	default:
		return a.exitErr(fmt.Errorf("unknown command: %s", args[0]))
	}
}

func parseUsageArgs(args []string) (usageOptions, error) {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	baseURL := fs.String("base-url", defaultBaseURL, "Local usage API base URL")
	timeout := fs.Duration("timeout", 5*time.Second, "HTTP timeout")
	jsonOut := fs.Bool("json", false, "emit JSON envelope")
	agentOut := fs.Bool("agent", false, "emit compact agent-friendly text")

	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if (arg == "--base-url" || arg == "--timeout") && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}

	if err := fs.Parse(flagArgs); err != nil {
		return usageOptions{}, err
	}
	opts := usageOptions{
		BaseURL: strings.TrimRight(*baseURL, "/"),
		Timeout: *timeout,
		JSON:    *jsonOut,
		Agent:   *agentOut,
		Action:  "all",
	}
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

func (a app) runUsage(client *http.Client, opts usageOptions) int {
	now := time.Now().UTC().Format(time.RFC3339)
	switch opts.Action {
	case "list":
		providers, err := fetchAll(client, opts.BaseURL)
		if err != nil {
			return a.renderUsageError(opts, err)
		}
		normalized := normalizeSnapshots(providers)
		if opts.JSON {
			a.writeJSON(jsonEnvelope{OK: true, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: now, ProviderCount: len(normalized), Providers: normalized})
			return 0
		}
		for _, provider := range normalized {
			fmt.Fprintln(a.stdout, provider.ProviderID)
		}
		return 0
	case "provider":
		snapshot, err := fetchOne(client, opts.BaseURL, opts.Target)
		if err != nil {
			return a.renderUsageError(opts, err)
		}
		normalized := normalizeSnapshot(snapshot)
		if opts.JSON {
			a.writeJSON(jsonEnvelope{OK: true, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: now, Provider: &normalized})
			return 0
		}
		if opts.Agent {
			a.printAgentProvider(normalized)
			return 0
		}
		a.printHumanProviders([]normalizedSnapshot{normalized})
		return 0
	default:
		providers, err := fetchAll(client, opts.BaseURL)
		if err != nil {
			return a.renderUsageError(opts, err)
		}
		normalized := normalizeSnapshots(providers)
		if opts.JSON {
			a.writeJSON(jsonEnvelope{OK: true, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: now, ProviderCount: len(normalized), Providers: normalized})
			return 0
		}
		if opts.Agent {
			for _, provider := range normalized {
				a.printAgentProvider(provider)
			}
			return 0
		}
		a.printHumanProviders(normalized)
		return 0
	}
}

func (a app) renderUsageError(opts usageOptions, err error) int {
	if opts.JSON {
		a.writeJSON(jsonEnvelope{OK: false, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: time.Now().UTC().Format(time.RFC3339), Error: err.Error()})
		return 1
	}
	return a.exitErr(err)
}

func fetchAll(client *http.Client, baseURL string) ([]apiSnapshot, error) {
	resp, err := client.Get(baseURL + "/v1/usage")
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, parseAPIError(resp)
	}
	var snapshots []apiSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshots); err != nil {
		return nil, fmt.Errorf("invalid JSON from /v1/usage: %w", err)
	}
	return snapshots, nil
}

func fetchOne(client *http.Client, baseURL, provider string) (apiSnapshot, error) {
	resp, err := client.Get(baseURL + "/v1/usage/" + provider)
	if err != nil {
		return apiSnapshot{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return apiSnapshot{}, errors.New("provider has no cached snapshot yet")
	}
	if resp.StatusCode != http.StatusOK {
		return apiSnapshot{}, parseAPIError(resp)
	}
	var snapshot apiSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return apiSnapshot{}, fmt.Errorf("invalid JSON from /v1/usage/%s: %w", provider, err)
	}
	return snapshot, nil
}

func parseAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if code, ok := payload["error"].(string); ok && code != "" {
			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("provider not found: %s", code)
			}
			return fmt.Errorf("api error (%d): %s", resp.StatusCode, code)
		}
	}
	return fmt.Errorf("api error (%d)", resp.StatusCode)
}

func normalizeSnapshots(in []apiSnapshot) []normalizedSnapshot {
	out := make([]normalizedSnapshot, 0, len(in))
	for _, snapshot := range in {
		out = append(out, normalizeSnapshot(snapshot))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return out
}

func normalizeSnapshot(in apiSnapshot) normalizedSnapshot {
	out := normalizedSnapshot{ProviderID: in.ProviderID, DisplayName: in.DisplayName, Plan: in.Plan, FetchedAt: in.FetchedAt}
	for _, line := range in.Lines {
		switch line.Type {
		case "progress":
			p := normalizedProgress{Label: line.Label, Used: line.Used, Limit: line.Limit, Unit: unitFor(line.Format), ResetsAt: line.ResetsAt, PeriodDurationMs: line.PeriodDurationMs}
			if line.Limit > 0 {
				remaining := line.Limit - line.Used
				percentUsed := (line.Used / line.Limit) * 100
				p.Remaining = &remaining
				p.PercentUsed = &percentUsed
			}
			out.Progress = append(out.Progress, p)
		case "text":
			out.Texts = append(out.Texts, normalizedText{Label: line.Label, Value: line.Value, Subtitle: line.Subtitle})
		case "badge":
			out.Badges = append(out.Badges, normalizedBadge{Label: line.Label, Text: line.Text, Subtitle: line.Subtitle})
		}
	}
	if len(out.Progress) > 0 {
		primary := out.Progress[0]
		out.PrimaryProgress = &primary
	}
	return out
}

func unitFor(format *lineFormat) string {
	if format == nil {
		return ""
	}
	switch format.Kind {
	case "count":
		return format.Suffix
	case "dollars":
		return "usd"
	default:
		return format.Kind
	}
}

func (a app) printHelp() {
	fmt.Fprintf(a.stdout, "hu — local AI usage checker\n\n")
	fmt.Fprintf(a.stdout, "Usage:\n")
	fmt.Fprintf(a.stdout, "  %s help\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s version\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage list\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage <providerId> [--agent] [--json]\n\n", a.progName)
	fmt.Fprintf(a.stdout, "Commands:\n")
	fmt.Fprintf(a.stdout, "  help       Show help\n")
	fmt.Fprintf(a.stdout, "  version    Show version\n")
	fmt.Fprintf(a.stdout, "  usage      Show configured provider usage\n")
}

func (a app) printUsageHelp() {
	fmt.Fprintf(a.stdout, "hu usage — inspect configured provider usage\n\n")
	fmt.Fprintf(a.stdout, "Usage:\n")
	fmt.Fprintf(a.stdout, "  %s usage\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage list\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage <providerId>\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage <providerId> --agent\n", a.progName)
	fmt.Fprintf(a.stdout, "  %s usage <providerId> --json\n\n", a.progName)
	fmt.Fprintf(a.stdout, "Flags:\n")
	fmt.Fprintf(a.stdout, "  --agent    compact agent-friendly text\n")
	fmt.Fprintf(a.stdout, "  --json     JSON envelope\n")
	fmt.Fprintf(a.stdout, "  --base-url custom local API base URL\n")
	fmt.Fprintf(a.stdout, "  --timeout  HTTP timeout\n")
}

func (a app) printHumanProviders(providers []normalizedSnapshot) {
	for i, provider := range providers {
		if i > 0 {
			fmt.Fprintln(a.stdout)
		}
		title := provider.DisplayName
		if title == "" {
			title = provider.ProviderID
		}
		fmt.Fprintf(a.stdout, "%s (%s)\n", title, provider.ProviderID)
		if provider.Plan != "" {
			fmt.Fprintf(a.stdout, "  plan      %s\n", provider.Plan)
		}
		for _, p := range provider.Progress {
			fmt.Fprintf(a.stdout, "  %-9s %s %6s", strings.ToLower(p.Label), progressBar(p.PercentUsed), percentString(p.PercentUsed))
			if p.Remaining != nil {
				fmt.Fprintf(a.stdout, "  %.0f/%.0f %s", p.Used, p.Limit, unitLabel(p.Unit))
			}
			if p.ResetsAt != "" {
				fmt.Fprintf(a.stdout, "  · reset %s", shortTime(p.ResetsAt))
			}
			fmt.Fprintln(a.stdout)
		}
		for _, text := range provider.Texts {
			fmt.Fprintf(a.stdout, "  %-9s %s\n", strings.ToLower(text.Label), text.Value)
		}
		for _, badge := range provider.Badges {
			fmt.Fprintf(a.stdout, "  %-9s %s\n", strings.ToLower(badge.Label), badge.Text)
		}
	}
}

func (a app) printAgentProvider(provider normalizedSnapshot) {
	title := provider.ProviderID
	if title == "" {
		title = provider.DisplayName
	}
	parts := []string{title}
	if provider.Plan != "" {
		parts = append(parts, "plan="+provider.Plan)
	}
	for _, p := range provider.Progress {
		parts = append(parts, fmt.Sprintf("%s=%s", slug(p.Label), percentString(p.PercentUsed)))
	}
	for _, t := range provider.Texts {
		parts = append(parts, fmt.Sprintf("%s=%s", slug(t.Label), sanitizeInline(t.Value)))
	}
	for _, b := range provider.Badges {
		parts = append(parts, fmt.Sprintf("%s=%s", slug(b.Label), sanitizeInline(b.Text)))
	}
	fmt.Fprintln(a.stdout, strings.Join(parts, " | "))
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
	if filled < 0 {
		filled = 0
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func percentString(percent *float64) string {
	if percent == nil {
		return "n/a"
	}
	return fmt.Sprintf("%5.1f%%", *percent)
}

func shortTime(value string) string {
	if value == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC().Format("2006-01-02 15:04Z")
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", value); err == nil {
		return t.UTC().Format("2006-01-02 15:04Z")
	}
	return value
}

func unitLabel(unit string) string {
	switch unit {
	case "", "percent":
		return ""
	default:
		return unit
	}
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func sanitizeInline(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.TrimSpace(value)
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
