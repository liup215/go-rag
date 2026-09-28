package embedder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEmbedURLJoin：base URL → 请求路径的拼接规则。
func TestEmbedURLJoin(t *testing.T) {
	cases := []struct {
		suffix string // 拼在 echo 服务器地址后的 base 后缀
		want   string // 期望的请求路径
	}{
		{"", "/v1/embeddings"},                          // 无版本段 → OpenAI 兼容默认
		{"/v1", "/v1/embeddings"},                       // OpenAI 标准风格
		{"/v1/", "/v1/embeddings"},                      // 尾随斜杠被裁掉
		{"/api/v1/custom", "/api/v1/custom/embeddings"}, // 含 /v1/ 自定义路径
		{"/api/plan/v3", "/api/plan/v3/embeddings"},     // Ark 风格 /vN 结尾
		{"/api/v2", "/api/v2/embeddings"},               // 其他 /vN
		{"/embeddings", "/embeddings"},                  // 完整路径原样使用
	}
	var got string
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1],"index":0}]}`))
		}))
		e := NewOpenAIEmbedder("k", srv.URL+c.suffix, "m")
		if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
			t.Errorf("base %q: Embed() error = %v", srv.URL+c.suffix, err)
		}
		if got != c.want {
			t.Errorf("base %q: got path %s, want %s", srv.URL+c.suffix, got, c.want)
		}
		srv.Close()
	}
}

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

func TestEmbedRetries429BeyondMaxAttempts(t *testing.T) {
	// 429 must retry indefinitely: even with MaxAttempts far below the
	// number of rate-limited responses, the batch still succeeds once the
	// server recovers.
	srv := new429Then200Server(t, 20)
	defer srv.Close()

	e := NewOpenAIEmbedder("k", srv.URL, "test-model")
	e.MaxAttempts = 3 // would give up after 3 attempts if 429 counted
	e.RetryBackoff = time.Millisecond
	e.MaxBackoff = 2 * time.Millisecond

	vecs, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed() error = %v, want success after 20 rate-limits", err)
	}
	if len(vecs) != 1 {
		t.Errorf("vecs = %v", vecs)
	}
}

func TestEmbedGivesUpAfterMaxAttemptsOn500(t *testing.T) {
	// Non-429 transient errors keep the bounded-retry contract.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("k", srv.URL, "test-model")
	e.MaxAttempts = 2
	e.RetryBackoff = time.Millisecond
	e.MaxBackoff = 2 * time.Millisecond

	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("Embed() should fail against a permanent 500")
	}
	if calls != 2 {
		t.Errorf("server called %d times, want exactly maxAttempts=2", calls)
	}
	if !strings.Contains(err.Error(), "embeddings API 500") {
		t.Errorf("error should mention 500: %v", err)
	}
}
