package azuredevops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := NewClient(ClientConfig{
		Organization: "org",
		ProjectID:    "project-id",
		RepositoryID: "repo-id",
		AuthMode:     pat,
		PAT:          "pat-token",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.httpClient = server.Client()
	client.baseURL = server.URL
	return client, server
}

func TestNewClient_Validation(t *testing.T) {
	tests := []struct {
		name   string
		config ClientConfig
	}{
		{name: "missing organization", config: ClientConfig{ProjectID: "p", RepositoryID: "r", AuthMode: pat, PAT: "token"}},
		{name: "PAT missing", config: ClientConfig{Organization: "o", ProjectID: "p", RepositoryID: "r", AuthMode: pat}},
		{name: "credential missing", config: ClientConfig{Organization: "o", ProjectID: "p", RepositoryID: "r", AuthMode: workloadIdentity}},
		{name: "unknown auth mode", config: ClientConfig{Organization: "o", ProjectID: "p", RepositoryID: "r", AuthMode: "oauth"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(tt.config); err == nil {
				t.Fatal("NewClient() error = nil, want error")
			}
		})
	}
}

func TestPostComment_CreatesMarkerThread(t *testing.T) {
	var createBody map[string]any
	client, server := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/org/project-id/_apis/git/repositories/repo-id/pullRequests/42" {
			writeJSON(w, map[string]string{"status": "active"})
			return
		}
		if r.URL.Path == "/org/project-id/_apis/git/repositories/repo-id/pullRequests/42/threads" {
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, map[string]any{"value": []any{}})
			case http.MethodPost:
				if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
					t.Fatalf("decode create body: %v", err)
				}
				writeJSON(w, map[string]any{"id": 7})
			default:
				t.Errorf("method = %s, want GET or POST", r.Method)
			}
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	if err := client.PostComment(context.Background(), 42, "preview"); err != nil {
		t.Fatalf("PostComment() error = %v", err)
	}
	if got := createBody["status"]; got != float64(1) {
		t.Errorf("status = %v, want 1", got)
	}
	comments := createBody["comments"].([]any)
	content := comments[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(content, CommentIdentifier+"\n\n") {
		t.Errorf("content = %q, missing marker", content)
	}
}

func TestPostComment_UpdatesExistingMarker(t *testing.T) {
	var updateBody comment
	client, server := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/org/project-id/_apis/git/repositories/repo-id/pullRequests/8":
			writeJSON(w, map[string]string{"status": "active"})
		case r.URL.Path == "/org/project-id/_apis/git/repositories/repo-id/pullRequests/8/threads":
			writeJSON(w, map[string]any{"value": []any{
				map[string]any{"id": 11, "comments": []any{map[string]any{"id": 12, "parentCommentId": 0, "content": CommentIdentifier + "\n\nold"}}},
			}})
		case r.Method == http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&updateBody); err != nil {
				t.Fatalf("decode update body: %v", err)
			}
			writeJSON(w, map[string]any{"id": 12})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	if err := client.PostComment(context.Background(), 8, "new"); err != nil {
		t.Fatalf("PostComment() error = %v", err)
	}
	if updateBody.ID != 12 || updateBody.Content != CommentIdentifier+"\n\nnew" {
		t.Errorf("update body = %+v", updateBody)
	}
}

func TestPostComment_RejectsDuplicateMarkers(t *testing.T) {
	client, server := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pullRequests/9") {
			writeJSON(w, map[string]string{"status": "active"})
			return
		}
		writeJSON(w, map[string]any{"value": []any{
			map[string]any{"id": 1, "comments": []any{map[string]any{"id": 1, "content": CommentIdentifier}}},
			map[string]any{"id": 2, "comments": []any{map[string]any{"id": 1, "content": CommentIdentifier}}},
		}})
	}))
	defer server.Close()

	if err := client.PostComment(context.Background(), 9, "preview"); err == nil || !strings.Contains(err.Error(), ErrConflict.Error()) {
		t.Fatalf("PostComment() error = %v, want conflict", err)
	}
}

func TestPostComment_DoesNotWriteClosedPR(t *testing.T) {
	var requests atomic.Int32
	client, server := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, map[string]string{"status": "completed"})
	}))
	defer server.Close()

	if err := client.PostComment(context.Background(), 10, "preview"); err == nil || !strings.Contains(err.Error(), ErrTerminalPR.Error()) {
		t.Fatalf("PostComment() error = %v, want terminal PR error", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("request count = %d, want 1", got)
	}
}

func TestRequest_RetriesUnauthorizedAfterInvalidatingToken(t *testing.T) {
	credential := &testCredential{tokens: []Token{{Value: "old", ExpiresAt: time.Now().Add(time.Hour)}, {Value: "new", ExpiresAt: time.Now().Add(time.Hour)}}}
	client, err := NewClient(ClientConfig{
		Organization: "org", ProjectID: "project-id", RepositoryID: "repo-id",
		AuthMode: workloadIdentity, Credential: credential,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if len(seen) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]string{"status": "active"})
	}))
	defer server.Close()
	client.httpClient = server.Client()
	client.baseURL = server.URL

	if _, err := client.getPullRequest(context.Background(), 1); err != nil {
		t.Fatalf("getPullRequest() error = %v", err)
	}
	if len(seen) != 2 || seen[0] != "Bearer old" || seen[1] != "Bearer new" {
		t.Errorf("authorization headers = %v", seen)
	}
	if credential.calls != 2 {
		t.Errorf("credential calls = %d, want 2", credential.calls)
	}
}

func TestDeleteComment_MissingCommentSucceeds(t *testing.T) {
	client, server := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"value": []any{}})
	}))
	defer server.Close()

	if err := client.DeleteComment(context.Background(), 4); err != nil {
		t.Fatalf("DeleteComment() error = %v, want nil", err)
	}
}

type testCredential struct {
	tokens []Token
	calls  int
}

func (c *testCredential) GetToken(context.Context) (Token, error) {
	token := c.tokens[min(c.calls, len(c.tokens)-1)]
	c.calls++
	return token, nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
