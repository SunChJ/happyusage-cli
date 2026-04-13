package happyusage

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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

type cliOptions struct {
	BaseURL  string
	Timeout  time.Duration
	JSON     bool
	Provider string
	Command  string
	ProgName string
}

func Main(progName string, args []string) int {
	opts, err := parseFlags(progName, args)
	if err != nil {
		return exitErr(err)
	}

	if opts.Command == "version" {
		fmt.Println(Version)
		return 0
	}

	client := &http.Client{Timeout: opts.Timeout}
	result, err := run(client, opts)
	if err != nil {
		if opts.JSON {
			writeJSON(jsonEnvelope{
				OK:        false,
				Source:    "local_usage_http_api",
				BaseURL:   strings.TrimRight(opts.BaseURL, "/"),
				CheckedAt: time.Now().UTC().Format(time.RFC3339),
				Error:     err.Error(),
			})
			return 1
		}
		return exitErr(err)
	}

	if opts.JSON {
		writeJSON(result)
		return 0
	}

	printHuman(result, opts)
	return 0
}

func parseFlags(progName string, args []string) (cliOptions, error) {
	fs := flag.NewFlagSet(progName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	baseURL := fs.String("base-url", defaultBaseURL, "Local usage API base URL")
	timeout := fs.Duration("timeout", 5*time.Second, "HTTP timeout")
	jsonOut := fs.Bool("json", false, "emit JSON")
	command := fs.String("command", "get", "get, providers, health, version")

	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}

	rest := fs.Args()
	provider := ""
	if len(rest) > 0 {
		provider = rest[0]
	}

	return cliOptions{
		BaseURL:  strings.TrimRight(*baseURL, "/"),
		Timeout:  *timeout,
		JSON:     *jsonOut,
		Provider: provider,
		Command:  *command,
		ProgName: progName,
	}, nil
}

func run(client *http.Client, opts cliOptions) (jsonEnvelope, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	switch opts.Command {
	case "health":
		providers, err := fetchAll(client, opts.BaseURL)
		if err != nil {
			return jsonEnvelope{}, err
		}
		return jsonEnvelope{OK: true, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: now, ProviderCount: len(providers)}, nil
	case "providers":
		providers, err := fetchAll(client, opts.BaseURL)
		if err != nil {
			return jsonEnvelope{}, err
		}
		normalized := normalizeSnapshots(providers)
		return jsonEnvelope{OK: true, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: now, ProviderCount: len(normalized), Providers: normalized}, nil
	case "get":
		if opts.Provider != "" {
			provider, err := fetchOne(client, opts.BaseURL, opts.Provider)
			if err != nil {
				return jsonEnvelope{}, err
			}
			normalized := normalizeSnapshot(provider)
			return jsonEnvelope{OK: true, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: now, Provider: &normalized}, nil
		}
		providers, err := fetchAll(client, opts.BaseURL)
		if err != nil {
			return jsonEnvelope{}, err
		}
		normalized := normalizeSnapshots(providers)
		return jsonEnvelope{OK: true, Source: "local_usage_http_api", BaseURL: opts.BaseURL, CheckedAt: now, ProviderCount: len(normalized), Providers: normalized}, nil
	default:
		return jsonEnvelope{}, fmt.Errorf("unsupported command: %s", opts.Command)
	}
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

func writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func printHuman(result jsonEnvelope, opts cliOptions) {
	if result.Provider != nil {
		printProvider(*result.Provider)
		return
	}
	if opts.Command == "health" {
		fmt.Printf("happyusage api ok · %d provider(s)\n", result.ProviderCount)
		return
	}
	for i, provider := range result.Providers {
		if i > 0 {
			fmt.Println()
		}
		printProvider(provider)
	}
}

func printProvider(provider normalizedSnapshot) {
	title := provider.DisplayName
	if title == "" {
		title = provider.ProviderID
	}
	fmt.Println(title)
	if provider.Plan != "" {
		fmt.Printf("  plan: %s\n", provider.Plan)
	}
	for _, progress := range provider.Progress {
		line := fmt.Sprintf("  %-12s %s", progress.Label+":", formatPercent(progress.PercentUsed))
		if progress.Remaining != nil {
			line += fmt.Sprintf(" used %.1f / %.1f", progress.Used, progress.Limit)
		}
		if progress.ResetsAt != "" {
			line += fmt.Sprintf(" · resets %s", progress.ResetsAt)
		}
		fmt.Println(line)
	}
	for _, text := range provider.Texts {
		fmt.Printf("  %-12s %s\n", text.Label+":", text.Value)
	}
	for _, badge := range provider.Badges {
		fmt.Printf("  %-12s %s\n", badge.Label+":", badge.Text)
	}
}

func formatPercent(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *v)
}

func exitErr(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
