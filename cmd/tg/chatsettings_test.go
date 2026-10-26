package main

import (
	"reflect"
	"testing"

	"github.com/go-faster/errors"

	"github.com/gotd/td/tg"
)

func TestApplyRights(t *testing.T) {
	current := tg.ChatBannedRights{SendPolls: true, EditRank: true, UntilDate: 42}

	next, err := applyRights(current, []string{"manage_topics", "pin_messages"}, []string{"send_polls"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := deniedRights(next), []string{"pin_messages", "manage_topics"}; !reflect.DeepEqual(got, want) {
		t.Errorf("denied = %v, want %v", got, want)
	}
	if !next.EditRank || next.UntilDate != 42 {
		t.Errorf("unmanaged fields lost: %+v", next)
	}
	if !current.SendPolls {
		t.Error("current was modified")
	}

	for _, tc := range []struct {
		name        string
		deny, allow []string
	}{
		{name: "unknown", deny: []string{"send_everything"}},
		{name: "conflict", deny: []string{"send_polls"}, allow: []string{"send_polls"}},
	} {
		_, err := applyRights(current, tc.deny, tc.allow)
		var u usageError
		if !errors.As(err, &u) {
			t.Errorf("%s: err = %v, want usage error", tc.name, err)
		}
	}
}

func TestRightNamesCoverFields(t *testing.T) {
	names := rightNames()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate right %q", n)
		}
		seen[n] = true
	}
	// Every listed right must map to its own flag.
	var r tg.ChatBannedRights
	for _, f := range rightFields(&r) {
		*f.flag = true
	}
	if got := deniedRights(r); len(got) != len(names) {
		t.Errorf("denied %d rights, want %d", len(got), len(names))
	}
}

func TestSplitNames(t *testing.T) {
	got := splitNames([]string{"a, b", "", "c,"})
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("splitNames = %v, want %v", got, want)
	}
}

func TestReactionsRequest(t *testing.T) {
	if r, err := reactionsRequest(true, false, nil); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(*tg.ChatReactionsAll); !ok {
		t.Errorf("--all = %T", r)
	}
	if r, err := reactionsRequest(false, true, nil); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(*tg.ChatReactionsNone); !ok {
		t.Errorf("--none = %T", r)
	}
	r, err := reactionsRequest(false, false, []string{"👍", "🔥"})
	if err != nil {
		t.Fatal(err)
	}
	mode, emoji := describeReactions(r)
	if mode != "some" || !reflect.DeepEqual(emoji, []string{"👍", "🔥"}) {
		t.Errorf("--emoji = %s %v", mode, emoji)
	}
	for _, bad := range [][]bool{{false, false}, {true, true}} {
		if _, err := reactionsRequest(bad[0], bad[1], nil); err == nil {
			t.Errorf("reactionsRequest(%v) accepted", bad)
		}
	}
	if _, err := reactionsRequest(true, false, []string{"👍"}); err == nil {
		t.Error("--all with --emoji accepted")
	}
}

func TestParseOnOff(t *testing.T) {
	for in, want := range map[string]bool{"on": true, "ON": true, "true": true, "off": false, "no": false, "0": false} {
		got, err := parseOnOff(in)
		if err != nil || got != want {
			t.Errorf("parseOnOff(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseOnOff("maybe"); err == nil {
		t.Error("parseOnOff(maybe) accepted")
	}
}

func TestChatToggleNames(t *testing.T) {
	want := []string{"antispam", "join-request", "join-to-send", "noforwards", "participants-hidden", "prehistory-hidden", "signatures"}
	if got := chatToggleNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("toggles = %v, want %v", got, want)
	}
}

func TestWrapRPC(t *testing.T) {
	if err := wrapRPC(nil, "x.y"); err != nil {
		t.Errorf("wrapRPC(nil) = %v, want nil", err)
	}
	if err := wrapRPC(errors.New("boom"), "x.y"); err == nil || err.Error() != "x.y: boom" {
		t.Errorf("wrapRPC = %v", err)
	}
}

func TestChannelSettings(t *testing.T) {
	ch := &tg.Channel{Megagroup: true, Forum: true, JoinToSend: true}
	ch.SetDefaultBannedRights(tg.ChatBannedRights{ManageTopics: true, ChangeInfo: true})
	full := &tg.ChannelFull{
		LinkedChatID:       3017672199,
		PinnedMsgID:        90,
		AdminsCount:        4,
		ChatPhoto:          &tg.Photo{ID: 1},
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "👍"}}},
	}
	s := channelSettings(ch, full)
	if !s.Megagroup || !s.Forum || !s.JoinToSend || !s.HasPhoto || s.LinkedChatID != 3017672199 || s.PinnedMsgID != 90 {
		t.Errorf("settings = %+v", s)
	}
	if want := []string{"change_info", "manage_topics"}; !reflect.DeepEqual(s.DefaultBannedRights, want) {
		t.Errorf("rights = %v, want %v", s.DefaultBannedRights, want)
	}
	if s.AvailableReactions != "some" || !reflect.DeepEqual(s.Reactions, []string{"👍"}) {
		t.Errorf("reactions = %s %v", s.AvailableReactions, s.Reactions)
	}
	fields := map[string]string{}
	for _, f := range s.fields() {
		fields[f[0]] = f[1]
	}
	if fields["linked_chat_id"] != "3017672199" || fields["available_reactions"] != "some 👍" || fields["forum"] != "yes" {
		t.Errorf("text fields = %v", fields)
	}
	if _, ok := fields["broadcast"]; ok {
		t.Error("unset field rendered")
	}
}
