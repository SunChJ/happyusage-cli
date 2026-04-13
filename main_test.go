package happyusage

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer() *httptest.Server {
	handler := http.NewServeMux()
	handler.HandleFunc("/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
			  "providerId":"claude",
			  "displayName":"Claude",
			  "plan":"Pro",
			  "fetchedAt":"2026-04-13T00:00:00Z",
			  "lines":[
			    {"type":"progress","label":"Session","used":25,"limit":100,"format":{"kind":"percent"},"resetsAt":"2026-04-13T07:00:00Z"},
			    {"type":"text","label":"Today","value":"$2.10 · 3M tokens"}
			  ]
			},
			{
			  "providerId":"codex",
			  "displayName":"Codex",
			  "plan":"Plus",
			  "fetchedAt":"2026-04-13T00:00:00Z",
			  "lines":[
			    {"type":"progress","label":"Weekly","used":50,"limit":100,"format":{"kind":"percent"}}
			  ]
			}
		]`))
	})
	handler.HandleFunc("/v1/usage/claude", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "providerId":"claude",
		  "displayName":"Claude",
		  "plan":"Pro",
		  "fetchedAt":"2026-04-13T00:00:00Z",
		  "lines":[
		    {"type":"progress","label":"Session","used":25,"limit":100,"format":{"kind":"percent"},"resetsAt":"2026-04-13T07:00:00Z"},
		    {"type":"text","label":"Today","value":"$2.10 · 3M tokens"}
		  ]
		}`))
	})
	return httptest.NewServer(handler)
}

func TestMainWithNoArgsShowsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := MainWithIO("hu", nil, &stdout, &stderr, &http.Client{})
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "hu — local AI usage checker") {
		t.Fatalf("unexpected help output: %s", stdout.String())
	}
}

func TestUsageListShowsProviderIDs(t *testing.T) {
	ts := testServer()
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	exitCode := MainWithIO("hu", []string{"usage", "list", "--base-url", ts.URL}, &stdout, &stderr, ts.Client())
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d, stderr=%s", exitCode, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "claude") || !strings.Contains(got, "codex") {
		t.Fatalf("expected provider ids, got %s", got)
	}
}

func TestUsageProviderAgentOutput(t *testing.T) {
	ts := testServer()
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	exitCode := MainWithIO("hu", []string{"usage", "claude", "--agent", "--base-url", ts.URL}, &stdout, &stderr, ts.Client())
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d, stderr=%s", exitCode, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "claude") || !strings.Contains(got, "session=") || !strings.Contains(got, "today=") {
		t.Fatalf("unexpected agent output: %s", got)
	}
}

func TestUsageProviderJSONOutput(t *testing.T) {
	ts := testServer()
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	exitCode := MainWithIO("hu", []string{"usage", "claude", "--json", "--base-url", ts.URL}, &stdout, &stderr, ts.Client())
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d, stderr=%s", exitCode, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "\"provider_id\": \"claude\"") || !strings.Contains(got, "\"source\": \"local_usage_http_api\"") {
		t.Fatalf("unexpected json output: %s", got)
	}
}
