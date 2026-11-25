package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExplicitProviderRoute(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/decisions" || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Cookie") != "" {
			t.Error("wrong upstream path or credential boundary")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"model":"allowed","questions":[]}` {
			t.Errorf("unexpected request body %q (%v)", body, err)
		}
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Set-Cookie", "upstream=secret")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit"}}`)
	}))
	defer upstream.Close()

	router := gin.New()
	router.POST("/enabled", explicitProviderRoute(upstream.URL+"/v1/decisions", "provider-key", "allowed", http.DefaultTransport))
	router.POST("/disabled", explicitProviderRoute(upstream.URL+"/v1/decisions", "", "allowed", http.DefaultTransport))
	for _, test := range []struct {
		path, body string
		status     int
	}{
		{"/disabled", `{"model":"allowed"}`, 503},
		{"/enabled", `{"model":"wrong"}`, 404},
		{"/enabled", `[1,2]`, 400},
		{"/enabled", strings.Repeat("x", explicitProviderBodyLimit+1), 413},
		{"/enabled", `{"model":"allowed","questions":[]}`, 429},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer client-key")
		request.Header.Set("Cookie", "client=secret")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s: got %d, want %d (%s)", test.path, recorder.Code, test.status, recorder.Body)
		}
		if test.status == 429 && (recorder.Header().Get("X-Request-Id") != "req_test" || recorder.Header().Get("Set-Cookie") != "" || !strings.Contains(recorder.Body.String(), "rate_limit")) {
			t.Fatal("provider error contract not preserved")
		}
	}
	if calls != 1 {
		t.Fatalf("expected one upstream call and no retries, got %d", calls)
	}
}
