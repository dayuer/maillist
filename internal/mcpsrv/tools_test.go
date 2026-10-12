package mcpsrv

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect opens an MCP session over the in-memory transport — the same protocol a client speaks over
// HTTP or stdio, with no network. It is how every test in this package talks to the server, so what
// they exercise is the real surface rather than a call to a handler.
func connect(t *testing.T, deps Deps, hash string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := New(deps)
	go func() {
		_ = server.For(hash).Run(ctx, serverTransport)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// listed is the tool list as a client sees it, keyed by name.
func listed(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		out[tool.Name] = tool
	}
	return out
}

// 设计 §9 的读写 tool 表就是这张表：名字逐字一致，一个不多、一个不少（run_cleanup 见执行记录）。
func TestTheSurfaceIsTheDesignsToolList(t *testing.T) {
	want := []string{
		// 读
		"list_newsletters", "get_newsletter", "list_campaigns", "get_campaign",
		"get_campaign_report", "get_stats", "list_contacts", "count_audience",
		"check_campaign", "list_domains", "list_suppressions", "get_job", "list_audit",
		// 写
		"create_newsletter", "update_newsletter", "create_campaign", "update_campaign",
		"send_test", "send_campaign", "schedule_campaign", "pause_campaign", "resume_campaign",
		"import_contacts", "add_suppression", "remove_suppression", "upload_image",
	}
	session := connect(t, Deps{}, "unused-hash")
	tools := listed(t, session)
	for _, name := range want {
		if _, ok := tools[name]; !ok {
			t.Errorf("%s is not registered", name)
		}
	}
	if len(tools) != len(want) {
		t.Errorf("the surface has %d tools, the design lists %d", len(tools), len(want))
	}
}

// D10：删除类与凭据类一个都不注册。看得见的表里没有，按名字调用也确实失败。
func TestDeletingAndCredentialsAreNotTools(t *testing.T) {
	forbidden := []string{
		"delete_campaign", "delete_newsletter", "delete_contact", "delete_upload",
		"create_credentials", "revoke_credentials", "list_credentials",
	}
	session := connect(t, Deps{}, "unused-hash")
	tools := listed(t, session)
	for _, name := range forbidden {
		if _, ok := tools[name]; ok {
			t.Errorf("%s is registered; design §9 keeps it off the MCP surface", name)
		}
		if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: name, Arguments: map[string]any{},
		}); err == nil {
			t.Errorf("calling %s succeeded", name)
		}
	}
}

// scope 与输入 schema 都从同一张表来：每个 tool 的 scope 与设计 §11 第 2 层一致。
func TestEveryToolDeclaresItsScope(t *testing.T) {
	want := map[string]string{}
	for _, name := range []string{
		"list_newsletters", "get_newsletter", "list_campaigns", "get_campaign",
		"get_campaign_report", "get_stats", "list_contacts", "count_audience",
		"check_campaign", "list_domains", "list_suppressions", "get_job", "list_audit",
	} {
		want[name] = "read"
	}
	for _, name := range []string{
		"create_newsletter", "update_newsletter", "create_campaign", "update_campaign",
		"add_suppression", "remove_suppression", "upload_image",
	} {
		want[name] = "write"
	}
	for _, name := range []string{
		"send_test", "send_campaign", "schedule_campaign", "pause_campaign", "resume_campaign",
		"import_contacts",
	} {
		want[name] = "send"
	}

	surface := Surface()
	if len(surface) != len(want) {
		t.Fatalf("the surface has %d tools, the design's scope table has %d", len(surface), len(want))
	}
	for _, info := range surface {
		if got := string(info.Scope); got != want[info.Name] {
			t.Errorf("%s needs %q, the design says %q", info.Name, got, want[info.Name])
		}
	}
}

// 凭据类与删除类不只是「没实现」，而是名字本身就不在这张表上。
func TestSurfaceHoldsNothingForbidden(t *testing.T) {
	for _, info := range Surface() {
		switch info.Name {
		case "create_credentials", "revoke_credentials", "list_credentials":
			t.Errorf("%s is on the surface", info.Name)
		}
		if len(info.Name) > 6 && info.Name[:7] == "delete_" {
			t.Errorf("%s is on the surface", info.Name)
		}
	}
}

// send_campaign 没有 force：不是「不理会它」，而是 schema 里根本没有这个属性，于是没人能传。
func TestSendCampaignHasNoForce(t *testing.T) {
	session := connect(t, Deps{}, "unused-hash")
	tools := listed(t, session)

	for _, name := range []string{"send_campaign", "resume_campaign", "schedule_campaign"} {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		// The schema has to be readable at all before "it has no force" means anything.
		if _, ok := schema.Properties["campaign"]; !ok {
			t.Fatalf("%s has no campaign argument: the schema read %s", name, encoded)
		}
		for property := range schema.Properties {
			if property == "force" || (len(property) >= 5 && property[:5] == "force") {
				t.Errorf("%s takes a %q argument; design §11 layer 0 has no way past the checks",
					name, property)
			}
		}
	}
}

// 读 tool 的分页上限只有一处实现，这里测它的三条边界。
func TestPageArgsEnforcesTheLimit(t *testing.T) {
	if limit, offset, err := (PageArgs{}).resolve(); err != nil || limit != DefaultPageSize || offset != 0 {
		t.Fatalf("defaults = %d %d %v", limit, offset, err)
	}
	if limit, _, err := (PageArgs{Limit: MaxPageSize}).resolve(); err != nil || limit != MaxPageSize {
		t.Fatalf("the maximum was refused: %d %v", limit, err)
	}
	if _, _, err := (PageArgs{Limit: MaxPageSize + 1}).resolve(); err == nil {
		t.Fatal("one row over the maximum was accepted")
	}
	if _, _, err := (PageArgs{Limit: -1}).resolve(); err == nil {
		t.Fatal("a negative limit was accepted")
	}
	if _, _, err := (PageArgs{Offset: -1}).resolve(); err == nil {
		t.Fatal("a negative offset was accepted")
	}
}

// has_more 是事实而不是猜测：恰好多一行时才为真，正好一整页的最后一页是 false。
func TestPaginateKnowsWhetherThereIsMore(t *testing.T) {
	rows := []int{1, 2, 3}
	full := paginate(rows, 2, 0)
	if !full.HasMore || full.Count != 2 || full.NextOffset != 2 {
		t.Fatalf("page = %+v", full)
	}
	last := paginate(rows[2:], 2, 2)
	if last.HasMore || last.Count != 1 || last.NextOffset != 0 {
		t.Fatalf("last page = %+v", last)
	}
	exact := paginate(rows[:2], 2, 0)
	if exact.HasMore {
		t.Fatalf("a page of exactly the limit claimed there was more: %+v", exact)
	}
}
