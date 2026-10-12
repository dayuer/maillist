package mcpsrv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"nibwire/internal/core/ids"
	"nibwire/internal/creds"
	"nibwire/internal/store"
	"nibwire/internal/uploads"
)

// postMCP sends one initialize request, which is the smallest thing that gets past the transport's
// own checks: everything else here is about whether the request reaches it at all.
func postMCP(t *testing.T, handler http.Handler, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// 数据库坏了不是"token 无效"：501/500 与 401 的区别就是排障的人该看哪里（#43）。
//
// A 401 here sends whoever is debugging after their credential while the store is the thing that is
// down — and a new token would not help.
func TestABrokenDatabaseIsNotAnInvalidToken(t *testing.T) {
	f := newFixture(t)
	handler := New(f.deps).HTTPHandler()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := postMCP(t, handler, "Bearer "+f.token)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("a closed database answered %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "Invalid token") {
		t.Errorf("the answer blames the token: %s", recorder.Body.String())
	}
}

// 没带 token 就被挡在门外，而且回的是一句能行动的话：401 且带 WWW-Authenticate。
func TestMCPEndpointNeedsAToken(t *testing.T) {
	f := newFixture(t)
	handler := New(f.deps).HTTPHandler()

	recorder := postMCP(t, handler, "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no token answered %d", recorder.Code)
	}
	if recorder.Header().Get("WWW-Authenticate") == "" {
		t.Error("a 401 without WWW-Authenticate is one a client cannot act on")
	}
	if !strings.Contains(recorder.Body.String(), "Bearer") {
		t.Errorf("body = %q", recorder.Body.String())
	}
	if recorder := postMCP(t, handler, "Bearer mc_not-a-real-token"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("an unknown token answered %d", recorder.Code)
	}
}

// api key 不在这扇门上：kind 是签发时的用途，两边都认这一条。
func TestAnAPIKeyIsRefusedOnMCP(t *testing.T) {
	f := newFixture(t)
	generated := creds.Generate()
	if err := f.store.CreateCredential(context.Background(), store.Credential{
		ID: ids.New(), Kind: creds.KindAPI, Name: "site", Prefix: generated.Prefix,
		TokenHash: generated.Hash, Scopes: []creds.Scope{creds.ScopeRead},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	apiToken := generated.Token

	recorder := postMCP(t, New(f.deps).HTTPHandler(), "Bearer "+apiToken)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("an api key answered %d, want 403", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "/api/v1") {
		t.Errorf("body = %q", recorder.Body.String())
	}
}

// 正确的 token 进得去：这一条同时是「前面两条不是碰巧拒绝」的证据。
func TestAValidTokenReachesTheTransport(t *testing.T) {
	f := newFixture(t)
	recorder := postMCP(t, New(f.deps).HTTPHandler(), "Bearer "+f.token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("a live mcp token answered %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Mcp-Session-Id") == "" {
		t.Errorf("no session id in %v", recorder.Header())
	}
}

// 端点的请求体上限是 10 MiB，且必须容得下工具自己承诺的额度：upload_image 收一张 4 MiB 的图片
// （base64 之后约 5.6 MiB，加上 JSON 信封），而 SDK 传输层的默认上限是 4 MiB——不抬它，一个走
// HTTP 的 agent 连自己权限内的图片都传不上来。
func TestTheEndpointAllowsTheLargestImageAndRefusesMoreThanTenMiB(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(New(f.deps).HTTPHandler())
	defer server.Close()

	// 一张顶满 uploads.MaxBytes 的 PNG，走真 HTTP（进程内 transport 不过这条限制）。
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, uploads.MaxBytes-8)...)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   server.URL,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: f.token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "upload_image",
		Arguments: map[string]any{
			"filename": "big.png",
			"data":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		},
	})
	if err != nil {
		t.Fatalf("uploading the largest allowed image over HTTP: %v", err)
	}
	if result.IsError {
		t.Fatalf("upload_image failed: %s", textOf(result))
	}

	// 过了 10 MiB 还是 413：端点是公网的，不该替一个匿名连接把内存押在解码器上。SDK 在读的时候
	// 拦（Content-Length、chunked 与 HTTP/2 一视同仁），这里给的是带 Content-Length 的过限请求。
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"probe","version":"1"},"pad":"` +
		strings.Repeat("a", 10<<20) + `"}}`
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+f.token)
	recorder := httptest.NewRecorder()
	New(f.deps).HTTPHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized body answered %d, want 413: %s", recorder.Code, recorder.Body.String())
	}
}

// 真客户端走真 HTTP：SDK 的 Streamable 客户端连上这个 handler，调一个 tool。
//
// 前面的测试用的是进程内 transport，这一条用的是 HTTP —— 中间隔着认证中间件、会话头与
// 事件流，是 agent 实际会走的那条路。
func TestAnHTTPClientCanCallATool(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 2)
	server := httptest.NewServer(New(f.deps).HTTPHandler())
	defer server.Close()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   server.URL,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: f.token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "count_audience", Arguments: map[string]any{"newsletter": "field-notes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("count_audience failed: %s", textOf(result))
	}
	var audience CountAudience
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &audience); err != nil {
		t.Fatal(err)
	}
	if audience.Active != 2 {
		t.Fatalf("count_audience = %+v", audience)
	}
}

// bearerRoundTripper is the one thing the SDK's HTTP client does not do for us: attach the token.
type bearerRoundTripper struct{ token string }

func (b bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	request.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(request)
}

// 撤销之后连会话都进不去：每请求查库（D8）就是这么用的。
func TestRevocationClosesTheHTTPDoor(t *testing.T) {
	f := newFixture(t)
	handler := New(f.deps).HTTPHandler()
	if recorder := postMCP(t, handler, "Bearer "+f.token); recorder.Code != http.StatusOK {
		t.Fatalf("the first call answered %d", recorder.Code)
	}
	credential, err := f.store.CredentialByName(context.Background(), "newsletter-bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RevokeCredential(context.Background(), credential.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if recorder := postMCP(t, handler, "Bearer "+f.token); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("after revocation the call answered %d", recorder.Code)
	}
}

// 401/403 的报文是 JSON，和 v1 API 一样的形状。
func TestAuthFailuresAnswerJSON(t *testing.T) {
	f := newFixture(t)
	recorder := postMCP(t, New(f.deps).HTTPHandler(), "Bearer nope")
	var parsed map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("body = %q: %v", recorder.Body.String(), err)
	}
	if parsed["error"] == "" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}
