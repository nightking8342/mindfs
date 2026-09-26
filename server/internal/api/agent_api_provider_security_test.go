package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProviderModelTestOpenAIBaseURLs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer upstream.Close()
	for _, suffix := range []string{"", "/", "/v1", "/v1/"} {
		t.Run(suffix, func(t *testing.T) {
			response, err := testOpenAICompatibleModel(context.Background(), upstream.URL+suffix, "secret", "m", "OK")
			if err != nil || response != "OK" {
				t.Fatalf("response=%q err=%v", response, err)
			}
		})
	}
}

func TestProviderGeminiTestUsesHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "secret" || r.URL.Query().Has("key") {
			t.Error("expected API key only in request header")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"OK"}]}}]}`))
	}))
	defer upstream.Close()
	response, err := testGeminiCompatibleModel(context.Background(), upstream.URL, "secret", "m", "OK")
	if err != nil || response != "OK" {
		t.Fatalf("response=%q err=%v", response, err)
	}
}

func TestProviderTestErrorsRedactSecrets(t *testing.T) {
	const secret = "secret-review-key+/=?"
	for _, scenario := range []string{"connection", "timeout", "http-error", "json-error"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(100 * time.Millisecond):
					}
					return
				}
				if scenario == "http-error" {
					w.WriteHeader(http.StatusUnauthorized)
				}
				// Exercise both raw HTTP errors and structured API errors.
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": strings.Repeat("x", 350) + secret + " " + url.QueryEscape(secret)}})
			}))
			defer upstream.Close()
			if scenario == "connection" {
				upstream.Close()
			}
			writeAPIProvidersFile(t, []agentAPIProvider{{ID: "p", BaseURL: upstream.URL, APIKey: secret, Protocols: []string{apiProviderProtocolGeminiCompatible}, Models: []string{"m"}}})
			ctx := context.Background()
			if scenario == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			result, err := testAgentAPIProviderModel(ctx, agentAPIProviderTestRequest{ProviderID: "p"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || result.Error == "" {
				t.Fatalf("expected failure: %+v", result)
			}
			if strings.Contains(result.Error, "secret-review") {
				t.Fatalf("secret leaked: %s", result.Error)
			}
			if scenario == "http-error" || scenario == "json-error" {
				if !strings.Contains(result.Error, "[REDACTED]") {
					t.Fatalf("missing redaction: %s", result.Error)
				}
			}
		})
	}
}

func TestProviderErrorRedactsEncodedSecretsBeforeTruncation(t *testing.T) {
	const secret = "secret-key+/=\""
	encoded, _ := json.Marshal(secret)
	for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), string(encoded[1 : len(encoded)-1])} {
		result := sanitizeAPIProviderError(strings.Repeat("x", 395)+value, secret)
		if strings.Contains(result, "secre") {
			t.Fatalf("partial secret exposed: %q", result)
		}
	}
}

func TestProviderSyncErrorsRedactSecrets(t *testing.T) {
	const secret = "secret-sync-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(strings.Repeat("x", 490) + secret))
	}))
	defer upstream.Close()
	writeAPIProvidersFile(t, []agentAPIProvider{{ID: "p", BaseURL: upstream.URL, APIKey: secret, Models: []string{"old"}}})
	_, results, err := syncAllAgentAPIProviders(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success {
		t.Fatalf("unexpected result: %+v", results)
	}
	if strings.Contains(results[0].Error, "secret") {
		t.Fatalf("sync leaked a secret: %s", results[0].Error)
	}
}
