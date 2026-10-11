package mailer

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Translated from lib/ses.js dnsRecords and loadSesDomains. The cache TTL is not covered here: it
// needs a stubbed SES endpoint, and the only thing it can get wrong inside the TTL is a stale list,
// which `domains --fresh` is for.
func TestIdentityFromSES(t *testing.T) {
	got := IdentityFromSES("example.net", &sesv2.GetEmailIdentityOutput{
		VerifiedForSendingStatus: true,
		DkimAttributes: &types.DkimAttributes{
			Status: types.DkimStatusSuccess,
			Tokens: []string{"s1", "s2"},
		},
		MailFromAttributes: &types.MailFromAttributes{
			MailFromDomain:       pointer("mail.example.net"),
			MailFromDomainStatus: types.MailFromDomainStatusSuccess,
		},
	}, "eu-west-1")

	if got.Domain != "example.net" || !got.VerifiedForSend {
		t.Fatalf("got %+v", got)
	}
	if got.DKIMStatus != "SUCCESS" {
		t.Errorf("DKIMStatus = %q, want SUCCESS", got.DKIMStatus)
	}
	if got.MailFromDomain != "mail.example.net" || got.MailFromStatus != "SUCCESS" {
		t.Errorf("mail from = %q / %q", got.MailFromDomain, got.MailFromStatus)
	}

	want := []DNSRecord{
		{Type: "CNAME", Name: "s1._domainkey.example.net", Value: "s1.dkim.amazonses.com"},
		{Type: "CNAME", Name: "s2._domainkey.example.net", Value: "s2.dkim.amazonses.com"},
		{Type: "MX", Name: "mail.example.net", Value: "10 feedback-smtp.eu-west-1.amazonses.com"},
		{Type: "TXT", Name: "mail.example.net", Value: "v=spf1 include:amazonses.com ~all"},
	}
	if len(got.Records) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(got.Records), len(want), got.Records)
	}
	for i := range want {
		if got.Records[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, got.Records[i], want[i])
		}
	}
}

// A domain without MAIL FROM configured has no MX/TXT rows: upstream only adds the bounce records
// when mailFromDomain is set.
func TestIdentityFromSESWithoutMailFrom(t *testing.T) {
	got := IdentityFromSES("example.net", &sesv2.GetEmailIdentityOutput{
		VerifiedForSendingStatus: false,
	}, "us-east-1")
	if len(got.Records) != 0 {
		t.Errorf("want no records, got %+v", got.Records)
	}
	if got.VerifiedForSend {
		t.Error("want not verified")
	}
}

func TestStaticIdentity(t *testing.T) {
	reader := StaticIdentity{Domains: []string{"example.net", " ", "other.example"}}
	identities, err := reader.Identities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 2 {
		t.Fatalf("got %+v, want two domains (the blank one is skipped)", identities)
	}
	if domains := VerifiedDomains(identities); len(domains) != 2 || domains[0] != "example.net" {
		t.Errorf("VerifiedDomains = %v", domains)
	}
}
