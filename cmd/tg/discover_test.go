package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestDiscoveredFromChats(t *testing.T) {
	ch := &tg.Channel{ID: 1, Title: "Makers", Username: "makers", Megagroup: true}
	ch.SetParticipantsCount(4200)
	news := &tg.Channel{ID: 2, Title: "News", Broadcast: true}
	basic := &tg.Chat{ID: 3, Title: "Old group", ParticipantsCount: 12}
	got := discoveredFromChats([]tg.ChatClass{ch, news, basic, &tg.ChatEmpty{ID: 4}})
	if len(got) != 3 {
		t.Fatalf("got %d peers, want 3: %+v", len(got), got)
	}
	if got[0].Kind != kindSupergroup || got[0].Members != 4200 || got[0].Username != "makers" {
		t.Fatalf("supergroup = %+v", got[0])
	}
	if got[1].Kind != kindBroadcast || got[1].Members != 0 {
		t.Fatalf("broadcast = %+v", got[1])
	}
	if got[2].Kind != kindGroup || got[2].Members != 12 || got[2].Type != peerChat {
		t.Fatalf("basic group = %+v", got[2])
	}
}

func TestDescribeDiscoveredBot(t *testing.T) {
	ent := entitiesOf([]tg.UserClass{&tg.User{ID: 7, Username: "helper_bot", Bot: true}}, nil)
	d := describeDiscovered(&tg.PeerUser{UserID: 7}, ent)
	if d.Kind != chatKindBot || d.Username != "helper_bot" {
		t.Fatalf("bot = %+v", d)
	}
}

// The discovered peers keep peerRef's flat JSON shape and only add fields.
func TestDiscoveredJSONIsAdditive(t *testing.T) {
	data, err := json.Marshal(discoveredListResult{Peers: []discoveredPeer{{
		peerRef: peerRef{ID: 1, Type: peerChannel, Name: "Makers", Username: "makers"}, Kind: kindSupergroup, Members: 5,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"peers":[{"id":1,"type":"channel","name":"Makers","username":"makers","kind":"supergroup","members":5}]}`
	if string(data) != want {
		t.Fatalf("json = %s\nwant %s", data, want)
	}
}

func TestDiscoveredText(t *testing.T) {
	var buf bytes.Buffer
	err := discoveredListResult{Peers: []discoveredPeer{
		{peerRef: peerRef{ID: 1, Type: peerChannel, Username: "makers"}, Kind: kindForum, Members: 9},
	}, Total: 40}.MarshalText(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"channel\t@makers\tid=1\tforum\tmembers=9", "(1 of 40 shown)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, buf.String())
		}
	}
}
