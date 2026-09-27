package embedder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryAfterParsing(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"120", 120 * time.Second},
		{"", 0},
		{"  15 ", 15 * time.Second},
		{"abc", 0}, // date-based Retry-After is tolerated by returning 0
		{"-5", 0},
	}
	for _, c := range cases {
		if got := retryAfter(c.in); got != c.want {
			t.Errorf("retryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// embedHandler: always 429 style from a stub server.
func new429Then200Server(t *testing.T, four29s int) *httptest.Server {
	t.Helper()
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= four29s {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2],"index":0}],"usage":{"total_tokens":1}}`))
	}))
}

func TestEmbedRetries429UntilSuccess(t *testing.T) {
	srv := new429Then200Server(t, 6) // fails 6 times -> succeeds on attempt 7
	defer srv.Close()

	e := NewOpenAIEmbedder("k", srv.URL, "test-model")
	e.RetryBackoff = time.Millisecond
	e.MaxBackoff = 2 * time.Millisecond

	vecs, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed() error = %v, want success after retries", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 2 {
		t.Errorf("vecs = %v", vecs)
	}
}

func TestEmbedGivesUpAfterMaxAttemptsOn429(t *testing.T) {
	srv := new429Then200Server(t, 100) // never succeeds
	defer srv.Close()

	e := NewOpenAIEmbedder("k", srv.URL, "test-model")
	e.MaxAttempts = 3
	e.RetryBackoff = time.Millisecond
	e.MaxBackoff = 2 * time.Millisecond

	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("Embed() should exhaust retries against a permanent 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention 429: %v", err)
	}
}
