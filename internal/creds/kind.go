package creds

// Kind is a credential's purpose: which entry it authenticates (design §11, layer 1).
//
// The two kinds carry the same scopes and the same budget shape, and are refused on each other's
// entry point. That is the whole of what the column means, and it is what makes
// `nibwire credentials list` worth reading: a key issued for a website's signup form is not a key
// an agent may drive the newsletter with, even though both are bearer tokens.
const (
	// KindAPI is a key for the v1 API: a site's signup form, a campaign draft from a generator.
	KindAPI = "api"
	// KindMCP is a token for the MCP surface, over stdio or Streamable HTTP.
	KindMCP = "mcp"
)
