package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestInviteHashOf(t *testing.T) {
	for _, tc := range []struct {
		in   string
		hash string
		ok   bool
	}{
		{"https://t.me/+AbCdEf123", "AbCdEf123", true},
		{"t.me/+AbCdEf123?x=1", "AbCdEf123", true},
		{"https://t.me/joinchat/HCt2SRBg", "HCt2SRBg", true},
		{"telegram.me/joinchat/Zz9/", "Zz9", true},
		{"+AbCdEf123", "AbCdEf123", true},
		{"+15551234567", "", false}, // a phone number
		{"https://t.me/somegroup", "", false},
		{"@somegroup", "", false},
		{"https://t.me/+", "", false},
	} {
		hash, ok := inviteHashOf(tc.in)
		if hash != tc.hash || ok != tc.ok {
			t.Errorf("inviteHashOf(%q) = %q, %v; want %q, %v", tc.in, hash, ok, tc.hash, tc.ok)
		}
	}
}

func TestChannelKind(t *testing.T) {
	for _, tc := range []struct {
		ch   tg.Channel
		want string
	}{
		{tg.Channel{Broadcast: true}, kindBroadcast},
		{tg.Channel{Megagroup: true}, kindSupergroup},
		{tg.Channel{Megagroup: true, Forum: true}, kindForum},
		{tg.Channel{Gigagroup: true, Megagroup: true}, kindGigagroup},
	} {
		if got := channelKind(&tc.ch); got != tc.want {
			t.Errorf("channelKind(%+v) = %q, want %q", tc.ch, got, tc.want)
		}
	}
}

func TestDeniedRights(t *testing.T) {
	got := deniedRights(tg.ChatBannedRights{EmbedLinks: true, SendMedia: true, UntilDate: 99})
	want := []string{"send_media", rightEmbedLinks}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("deniedRights = %v, want %v", got, want)
	}
	if got := deniedRights(tg.ChatBannedRights{}); got != nil {
		t.Fatalf("no banned rights: got %v, want nil", got)
	}
}

func TestBuildChannelInspect(t *testing.T) {
	ch := &tg.Channel{
		ID: 10, Title: "Sellers", Username: "sellers", Megagroup: true, Left: true, Date: 1700000000,
		JoinRequest: true,
	}
	ch.SetParticipantsCount(50)
	ch.SetDefaultBannedRights(tg.ChatBannedRights{EmbedLinks: true})
	full := &tg.ChannelFull{
		ID: 10, About: "Rules in the pin", ParticipantsCount: 1234, OnlineCount: 56, AdminsCount: 3,
		SlowmodeSeconds: 300, PinnedMsgID: 77, SendPaidMessagesStars: 5, Antispam: true,
		BotInfo: []tg.BotInfo{{}},
	}
	full.SetLinkedChatID(20)
	var bi tg.BotInfo
	bi.SetUserID(30)
	full.BotInfo = []tg.BotInfo{bi}
	users := []tg.UserClass{&tg.User{ID: 30, Username: "guard_bot", Bot: true}}
	chats := []tg.ChatClass{&tg.Channel{ID: 20, Title: "Sellers News", Username: "sellersnews", Broadcast: true}}

	var res inspectResult
	buildChannelInspect(&res, ch, full, users, chats)

	if res.Kind != kindSupergroup || res.Member || res.CreatedAt != 1700000000 || res.JoinedAt != 0 {
		t.Fatalf("kind/member/dates wrong: %+v", res)
	}
	if res.Members != 1234 || res.Online != 56 || res.Admins != 3 {
		t.Fatalf("counts wrong: %+v", res)
	}
	if res.SlowmodeSeconds != 300 || res.PaidMessageStars != 5 || !res.JoinRequest || !res.Antispam {
		t.Fatalf("posting rules wrong: %+v", res)
	}
	if strings.Join(res.MembersCannot, ",") != rightEmbedLinks {
		t.Fatalf("members_cannot = %v", res.MembersCannot)
	}
	if res.LinkedChat == nil || res.LinkedChat.Username != "sellersnews" {
		t.Fatalf("linked chat = %+v", res.LinkedChat)
	}
	if len(res.Bots) != 1 || res.Bots[0].Username != "guard_bot" {
		t.Fatalf("bots = %+v", res.Bots)
	}
	if res.Pinned == nil || res.Pinned.ID != 77 {
		t.Fatalf("pinned = %+v", res.Pinned)
	}
}

func TestBuildChannelInspectMemberWithoutFull(t *testing.T) {
	ch := &tg.Channel{ID: 1, Title: "News", Broadcast: true, Date: 1710000000}
	ch.SetBannedRights(tg.ChatBannedRights{SendMessages: true, UntilDate: 1720000000})
	var res inspectResult
	buildChannelInspect(&res, ch, nil, nil, nil)
	if !res.Member || res.JoinedAt != 1710000000 || res.CreatedAt != 0 {
		t.Fatalf("member dates wrong: %+v", res)
	}
	if strings.Join(res.ICannot, ",") != "send_messages" || res.ICannotUntil != 1720000000 {
		t.Fatalf("own restrictions wrong: %+v", res)
	}
}

func TestBuildInviteInspect(t *testing.T) {
	inv := &tg.ChatInvite{Title: "Private Sellers", About: "no spam", ParticipantsCount: 900, Channel: true, Megagroup: true, RequestNeeded: true}
	inv.SetSubscriptionPricing(tg.StarsSubscriptionPricing{Period: 30 * 86400, Amount: 250})
	var res inspectResult
	buildInviteInspect(&res, inv)
	if !res.Preview || res.Kind != kindSupergroup || res.Members != 900 || !res.JoinRequest {
		t.Fatalf("invite preview wrong: %+v", res)
	}
	if res.SubscriptionStars != 250 || res.SubscriptionDays != 30 {
		t.Fatalf("subscription wrong: %+v", res)
	}
}

func TestInspectResultJSONOmitsUnknown(t *testing.T) {
	data, err := json.Marshal(inspectResult{Input: "@x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"input":"@x","member":false}` {
		t.Fatalf("json = %s", got)
	}
}

func TestInspectListText(t *testing.T) {
	ref := peerRef{ID: 10, Type: peerChannel, Name: "Sellers", Username: "sellers"}
	var buf bytes.Buffer
	err := inspectListResult{Results: []inspectResult{
		{Input: "@sellers", Peer: &ref, Kind: kindSupergroup, Members: 10, SlowmodeSeconds: 60, MembersCannot: []string{rightEmbedLinks},
			Pinned: &pinnedInfo{ID: 3, Text: "No ads\nexcept Fridays"}},
		{Input: "@gone", Error: &errorInfo{Message: "not found"}},
		{Input: "@later", Skipped: true},
	}}.MarshalText(&buf)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"@sellers [supergroup] id=10", "members: 10", "slow mode: 1m0s", "members cannot: embed_links", "pinned #3: No ads except Fridays", "@gone: error: not found", "@later: skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q:\n%s", want, out)
		}
	}
}

func TestInspectPrivateChannelIsLimited(t *testing.T) {
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		if _, ok := req.(*tg.ChannelsGetFullChannelRequest); !ok {
			t.Fatalf("unexpected request %T", req)
		}
		return nil, tgerr.New(400, "CHANNEL_PRIVATE")
	})
	ch := &tg.Channel{ID: 1, AccessHash: 2, Title: "Private", Megagroup: true, Left: true}
	res, err := inspectChatClass(context.Background(), api, inspectResult{Input: "x"}, ch, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !res.Limited || res.Kind != kindSupergroup || res.Peer == nil || res.Peer.Name != "Private" {
		t.Fatalf("res = %+v", res)
	}
}

func TestInspectForbiddenChannelFails(t *testing.T) {
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		t.Fatalf("unexpected request %T", req)
		return nil, nil
	})
	res, err := inspectChatClass(context.Background(), api, inspectResult{Input: "x"}, &tg.ChannelForbidden{ID: 3, Title: "Gone", Megagroup: true}, true)
	var nf notFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want not found", err)
	}
	if res.Peer == nil || res.Peer.ID != 3 || res.Kind != kindSupergroup {
		t.Fatalf("partial result lost: %+v", res)
	}
}

func TestInspectChannelWithPin(t *testing.T) {
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.ChannelsGetFullChannelRequest:
			return &tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 1, PinnedMsgID: 9, SlowmodeSeconds: 60, ChatPhoto: &tg.PhotoEmpty{}}}, nil
		case *tg.ChannelsGetMessagesRequest:
			if len(r.ID) != 1 {
				t.Fatalf("ids = %v", r.ID)
			}
			return &tg.MessagesChannelMessages{Messages: []tg.MessageClass{&tg.Message{ID: 9, Date: 77, Message: "Rules: no ads", PeerID: &tg.PeerChannel{ChannelID: 1}}}}, nil
		default:
			t.Fatalf("unexpected request %T", req)
			return nil, nil
		}
	})
	ch := &tg.Channel{ID: 1, AccessHash: 2, Title: "G", Username: "g", Megagroup: true}
	res, err := inspectChatClass(context.Background(), api, inspectResult{Input: "@g"}, ch, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pinned == nil || res.Pinned.Text != "Rules: no ads" || res.Pinned.Date != 77 || res.SlowmodeSeconds != 60 {
		t.Fatalf("res = %+v pinned %+v", res, res.Pinned)
	}
}

func TestBuildChatInspectDates(t *testing.T) {
	var res inspectResult
	buildChatInspect(&res, &tg.Chat{ID: 5, Title: "Old", Date: 1600000000, ParticipantsCount: 4}, nil, nil)
	if !res.Member || res.CreatedAt != 1600000000 || res.JoinedAt != 0 || res.Kind != kindGroup {
		t.Fatalf("res = %+v", res)
	}
}
