package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

// TestParticipantPeers checks that the users a response carries only as
// inviters, promoters or kickers are not listed as participants.
func TestParticipantPeers(t *testing.T) {
	const adminID, botID, memberID = 1, 2, 3
	res := &tg.ChannelsChannelParticipants{
		Participants: []tg.ChannelParticipantClass{
			&tg.ChannelParticipantBanned{
				Peer: &tg.PeerUser{UserID: botID}, KickedBy: adminID, BannedRights: tg.ChatBannedRights{ViewMessages: true},
			},
			&tg.ChannelParticipant{UserID: memberID},
			&tg.ChannelParticipantLeft{Peer: &tg.PeerChannel{ChannelID: 9}},
		},
		Users: []tg.UserClass{
			&tg.User{ID: adminID, Username: "admin"},
			&tg.User{ID: botID, Username: "oldbot", Bot: true},
			&tg.User{ID: memberID, Username: "member"},
		},
		Chats: []tg.ChatClass{&tg.Channel{ID: 9, Title: "Spam channel"}},
	}
	got := participantPeers(res).Peers
	if len(got) != 3 {
		t.Fatalf("got %d peers, want 3: %+v", len(got), got)
	}
	if got[0].Username != "oldbot" || got[1].Username != "member" || got[2].ID != 9 {
		t.Errorf("peers = %+v", got)
	}
	for _, p := range got {
		if p.Username == "admin" {
			t.Errorf("kicker listed as participant: %+v", p)
		}
	}
}
