package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
)

func TestExplicitOpenRouterCatalog(t *testing.T) {
	t.Setenv("OPENROUTER_EMBEDDINGS_API_KEY", "provider-key")
	t.Setenv("OPENROUTER_EMBEDDINGS_MODEL", "mongodb/voyage-4-lite")
	t.Setenv("OPENROUTER_DECISIONS_API_KEY", "provider-key")
	t.Setenv("OPENROUTER_DECISIONS_MODEL", "typesafe/jev-1.14")

	for _, test := range []struct {
		path, ua, body, header string
		status                 int
	}{
		{"/v1/models", "", "", "", 200},
		{"/v1/models/mongodb/voyage-4-lite", "", "", "", 200},
		{"/v1/models/typesafe/jev-1.14", "", "", "", 200},
		{"/v1/models/unconfigured/model", "", "", "", 404},
		{"/v1/models?client_version=cpa", "", "", "", 200},
		{"/v1/models", "claude-cli", "", "2023-06-01", 200},
		{"/v1/chat/completions", "", `{"model":"mongodb/voyage-4-lite","messages":[{"role":"user","content":"do not route"}]}`, "", 400},
	} {
		server := newTestServer(t)
		method := http.MethodGet
		if test.body != "" {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, test.path, strings.NewReader(test.body))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("User-Agent", test.ua)
		req.Header.Set("Anthropic-Version", test.header)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != test.status && !(method == http.MethodPost && rr.Code == 404) {
			t.Fatalf("%s: got %d, want %d: %s", test.path, rr.Code, test.status, rr.Body.String())
		}
		switch {
		case test.path == "/v1/models" && test.header == "":
			for _, id := range []string{"mongodb/voyage-4-lite", "typesafe/jev-1.14"} {
				if strings.Count(rr.Body.String(), `"`+id+`"`) != 1 {
					t.Fatalf("expected one record for %s in %s", id, rr.Body.String())
				}
			}
		case strings.HasPrefix(test.path, "/v1/models/") && test.status == 200:
			var model struct {
				ID, Object string
				OwnedBy    string `json:"owned_by"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &model); err != nil || model.ID != strings.TrimPrefix(test.path, "/v1/models/") || model.Object != "model" || model.OwnedBy != "openrouter" {
				t.Fatalf("bad model detail: %s", rr.Body.String())
			}
		case test.status == 200:
			if strings.Contains(rr.Body.String(), "mongodb/voyage-4-lite") || strings.Contains(rr.Body.String(), "typesafe/jev-1.14") {
				t.Fatalf("paid model leaked into alternate catalog: %s", rr.Body.String())
			}
		}
	}
	t.Setenv("OPENROUTER_DECISIONS_API_KEY", "")
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models/typesafe/jev-1.14", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("unconfigured provider unexpectedly discoverable: %d", rr.Code)
	}
}
