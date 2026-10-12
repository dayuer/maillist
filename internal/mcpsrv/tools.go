package mcpsrv

import (
	"mailcheap/internal/creds"
)

// tools is the MCP surface: every tool, the scope a call needs, and how it binds its handler.
//
// It is the whole surface, in one place, and it is deliberately short of three things design §9
// names: delete_* and the credential tools are not here at all (D10 — an agent can write and send,
// but it cannot remove anything or issue itself another key), and the name and the scope of a tool
// are declared exactly once, here, so a handler cannot be registered without one.
var tools = []*tool{
	// --- read (design §9, first list) ---
	{name: "list_newsletters", scope: creds.ScopeRead,
		about: "List the newsletters this deployment has, with paging.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, listNewsletters) }},
	{name: "get_newsletter", scope: creds.ScopeRead,
		about: "One newsletter, by id or slug.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, getNewsletter) }},
	{name: "list_campaigns", scope: creds.ScopeRead,
		about: "List campaigns, most recently active first.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, listCampaigns) }},
	{name: "get_campaign", scope: creds.ScopeRead,
		about: "One campaign, by id or by the subject of a draft.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, getCampaign) }},
	{name: "get_campaign_report", scope: creds.ScopeRead,
		about: "How an issue did: sent, delivered, opened, clicked, bounced, complained, and how " +
			"many of the most engaged have not had it yet.",
		add: func(sf *surface, t *tool) { addTool(sf, t, getCampaignReport) }},
	{name: "get_stats", scope: creds.ScopeRead,
		about: "Subscriber growth, sends by hour and the last 30 days' email rates, against the 30 " +
			"before them. The same figures `mailcheap stats` prints.",
		add: func(sf *surface, t *tool) { addTool(sf, t, getStats) }},
	{name: "list_contacts", scope: creds.ScopeRead,
		about: "List a newsletter's contacts, most recently subscribed first.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, listContacts) }},
	{name: "count_audience", scope: creds.ScopeRead,
		about: "How many people a newsletter or a campaign would reach, without sending anything. " +
			"Ask this before deciding to send.",
		add: func(sf *surface, t *tool) { addTool(sf, t, countAudience) }},
	{name: "check_campaign", scope: creds.ScopeRead,
		about: "The pre-send checklist. A blocking failure means send_campaign will refuse it too.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, checkCampaign) }},
	{name: "list_domains", scope: creds.ScopeRead,
		about: "The sending domains SES has verified, with any DNS records still to publish.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, listDomains) }},
	{name: "list_suppressions", scope: creds.ScopeRead,
		about: "The addresses this deployment never sends to, with why.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, listSuppressions) }},
	{name: "get_job", scope: creds.ScopeRead,
		about: "What became of a queued send. send_campaign answers with a job id; poll it here.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, getJob) }},
	{name: "list_audit", scope: creds.ScopeRead,
		about: "The audit log: every write and every send, whoever made it.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, listAudit) }},

	// --- write (design §9, second list) ---
	{name: "create_newsletter", scope: creds.ScopeWrite,
		about: "Create a newsletter.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, createNewsletter) }},
	{name: "update_newsletter", scope: creds.ScopeWrite,
		about: "Change a newsletter's settings. Fields you leave out are left as they are.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, updateNewsletter) }},
	{name: "create_campaign", scope: creds.ScopeWrite,
		about: "Create a draft from Markdown or HTML. Nothing is sent by this.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, createCampaign) }},
	{name: "update_campaign", scope: creds.ScopeWrite,
		about: "Change a campaign's draft. Fields you leave out are left as they are.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, updateCampaign) }},
	{name: "send_test", scope: creds.ScopeSend,
		about: "Send one test copy to one address, now, and answer with what happened. The subject " +
			"gets a [Test] prefix and the message stays out of every report.",
		add: func(sf *surface, t *tool) { addTool(sf, t, sendTest) }},
	{name: "send_campaign", scope: creds.ScopeSend,
		about: "Queue a campaign for the running server to send, and answer with the job id. The " +
			"pre-send checks run first; a blocking one refuses the send and there is no way past it.",
		add: func(sf *surface, t *tool) { addTool(sf, t, sendCampaign) }},
	{name: "schedule_campaign", scope: creds.ScopeSend,
		about: "Queue a draft for a chosen time, and answer with the job id.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, scheduleCampaign) }},
	{name: "pause_campaign", scope: creds.ScopeSend,
		about: "Stop a campaign that is sending. Claimed messages come back to the queue.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, pauseCampaign) }},
	{name: "resume_campaign", scope: creds.ScopeSend,
		about: "Send the rest of a paused campaign, with the audience it already has.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, resumeCampaign) }},
	{name: "import_contacts", scope: creds.ScopeSend,
		about: "Import contacts the caller already has, up to 1000 rows per call. A status only ever " +
			"moves stricter: an import cannot resurrect somebody who unsubscribed.",
		add: func(sf *surface, t *tool) { addTool(sf, t, importContacts) }},
	{name: "add_suppression", scope: creds.ScopeWrite,
		about: "Stop every newsletter from sending to one address.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, addSuppression) }},
	{name: "remove_suppression", scope: creds.ScopeWrite,
		about: "Take an address off the suppression list.",
		add:   func(sf *surface, t *tool) { addTool(sf, t, removeSuppression) }},
	{name: "upload_image", scope: creds.ScopeWrite,
		about: "Store an image and answer with its URL, which is what goes in a campaign body. Only " +
			"PNG, JPEG, GIF and WebP are accepted, decided by the bytes rather than the name.",
		add: func(sf *surface, t *tool) { addTool(sf, t, uploadImage) }},
}

// registerTools walks the table. Registering and listing are the same act, which is why the tests can
// read Surface() and trust it.
func registerTools(sf *surface, _ Deps) {
	for _, t := range tools {
		t.add(sf, t)
	}
}
