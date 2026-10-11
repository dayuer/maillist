package model

import "testing"

func TestOnlyDraftScheduledPausedAreQueueable(t *testing.T) {
	for s, want := range map[CampaignStatus]bool{
		CampaignDraft: true, CampaignScheduled: true, CampaignPaused: true,
		CampaignSending: false, CampaignSent: false, CampaignCanceled: false, CampaignFailed: false,
	} {
		if s.Queueable() != want {
			t.Errorf("%s.Queueable() = %v", s, !want)
		}
	}
}
