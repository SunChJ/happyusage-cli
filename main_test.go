package happyusage

import (
	"bytes"
	"strings"
	"testing"
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

func numPtr(v float64) *float64 { return &v }
