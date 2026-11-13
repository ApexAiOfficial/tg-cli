package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestParseNewBotLink(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want newBotLink
		ok   bool
	}{
		{"https://t.me/newbot/ManagerBot/CoolAIAgentBot?name=Cool+AI+Agent", newBotLink{"@ManagerBot", "CoolAIAgentBot", "Cool AI Agent"}, true},
		{"t.me/newbot/ManagerBot/shop_bot", newBotLink{"@ManagerBot", "shop_bot", ""}, true},
		{"https://telegram.me/newbot/M_bot/S_bot?name=My%20Shop", newBotLink{"@M_bot", "S_bot", "My Shop"}, true},
		{"https://t.me/ManagerBot?start=x", newBotLink{}, false},
		{"https://t.me/newbot/ManagerBot", newBotLink{}, false},
		{"https://t.me/newbot/ManagerBot/a/b", newBotLink{}, false},
		{"https://example.com/newbot/M/S", newBotLink{}, false},
		{"ftp://t.me/newbot/M/S", newBotLink{}, false},
		{"@ManagerBot", newBotLink{}, false},
	} {
		got, ok := parseNewBotLink(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseNewBotLink(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidateNewBot(t *testing.T) {
	for _, tc := range []struct {
		name, username string
		ok             bool
	}{
		{"My Shop", "MyShopBot", true},
		{"My Shop", "my_shop_bot", true},
		{"x", "abBOT", true},
		{"x", "a_bot", true},
		{"x", "abot", false},                                // 4 characters
		{"x", "a" + strings.Repeat("b", 28) + "bot", true},  // 32 characters
		{"x", "a" + strings.Repeat("b", 29) + "bot", false}, // 33 characters
		{"x", "1shopbot", false},                            // starts with a digit
		{"x", "my-shop-bot", false},
		{"x", "MyShop", false},
		{"", "MyShopBot", false},
		{"   ", "MyShopBot", false},
		{strings.Repeat("n", 65), "MyShopBot", false},
		{strings.Repeat("é", 64), "MyShopBot", true}, // runes, not bytes
	} {
		err := validateNewBot(tc.name, tc.username)
		if (err == nil) != tc.ok {
			t.Errorf("validateNewBot(%q, %q) = %v, want ok=%v", tc.name, tc.username, err, tc.ok)
		}
		var ue usageError
		if err != nil && !errors.As(err, &ue) {
			t.Errorf("validateNewBot(%q, %q): %v is not a usage error", tc.name, tc.username, err)
		}
	}
}

func TestWriteBotToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bot.token")
	if err := writeBotToken(path, "123:abc"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(path); string(b) != "123:abc\n" {
		t.Errorf("content = %q", b)
	}
	// Never replaces an existing file.
	if err := writeBotToken(path, "456:def"); err == nil {
		t.Error("overwrote an existing token file")
	}
	if b, _ := os.ReadFile(path); string(b) != "123:abc\n" {
		t.Errorf("content changed to %q", b)
	}
	for _, bad := range []string{"", "1:a\nb"} {
		if err := writeBotToken(filepath.Join(dir, "x"+bad), bad); err == nil {
			t.Errorf("accepted token %q", bad)
		}
	}
}

func createBotButton(managed bool) *tg.KeyboardButtonRequestPeer {
	pt := &tg.RequestPeerTypeCreateBot{BotManaged: managed}
	pt.SetSuggestedName("My Shop")
	pt.SetSuggestedUsername("MyShopBot")
	return &tg.KeyboardButtonRequestPeer{Text: "Create my store bot", ButtonID: 7, PeerType: pt, MaxQuantity: 1}
}

func TestRequestPeerButtonItem(t *testing.T) {
	item := newButtonItem(createBotButton(true))
	if item.Type != "request_peer" || item.RequestPeer == nil {
		t.Fatalf("item = %+v", item)
	}
	want := requestPeerItem{ButtonID: 7, Kind: "create_bot", BotManaged: true, SuggestedName: "My Shop", SuggestedUsername: "MyShopBot", MaxQuantity: 1}
	if *item.RequestPeer != want {
		t.Errorf("request_peer = %+v, want %+v", *item.RequestPeer, want)
	}
	user := newButtonItem(&tg.KeyboardButtonRequestPeer{Text: "Pick", ButtonID: 1, PeerType: &tg.RequestPeerTypeUser{}})
	if user.RequestPeer == nil || user.RequestPeer.Kind != "user" {
		t.Errorf("user request = %+v", user.RequestPeer)
	}
}

func pressCreateBot(t *testing.T, api *tg.Client, b *tg.KeyboardButtonRequestPeer, opts pressOptions) (clickResult, error) {
	t.Helper()
	btn := keyboardButton{Index: 1, Row: 1, Col: 1, raw: b, item: newButtonItem(b)}
	return pressButton(context.Background(), api, message.NewSender(api).To(botPeer()), botPeer(), 42, btn, opts)
}

func TestPressCreateBotButton(t *testing.T) {
	var (
		create *tg.BotsCreateBotRequest
		share  *tg.MessagesSendBotRequestedPeerRequest
	)
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.BotsCreateBotRequest:
			create = r
			return &tg.User{ID: 900, AccessHash: 9, Bot: true, FirstName: r.Name, Username: r.Username}, nil
		case *tg.MessagesSendBotRequestedPeerRequest:
			share = r
			return &tg.Updates{}, nil
		}
		return nil, errors.New("unexpected request")
	})

	res, err := pressCreateBot(t, api, createBotButton(true), pressOptions{botUsername: "Other_bot"})
	if err != nil {
		t.Fatal(err)
	}
	if create == nil || create.Name != "My Shop" || create.Username != "Other_bot" || create.ViaDeeplink {
		t.Fatalf("create = %+v", create)
	}
	if mgr, ok := create.ManagerID.(*tg.InputUser); !ok || mgr.UserID != testBotID || mgr.AccessHash != 1 {
		t.Errorf("manager = %#v, want the asking bot", create.ManagerID)
	}
	if share == nil || share.ButtonID != 7 || share.MsgID != 42 || len(share.RequestedPeers) != 1 {
		t.Fatalf("share = %+v", share)
	}
	if p, ok := share.RequestedPeers[0].(*tg.InputPeerUser); !ok || p.UserID != 900 || p.AccessHash != 9 {
		t.Errorf("shared peer = %#v", share.RequestedPeers[0])
	}
	if res.CreatedBot == nil || res.CreatedBot.ID != 900 || res.CreatedBot.Username != "Other_bot" {
		t.Errorf("created = %+v", res.CreatedBot)
	}
}

func TestPressCreateBotButtonShareFails(t *testing.T) {
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		if r, ok := req.(*tg.BotsCreateBotRequest); ok {
			return &tg.User{ID: 900, Bot: true, Username: r.Username}, nil
		}
		return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
	})
	res, err := pressCreateBot(t, api, createBotButton(true), pressOptions{})
	if err == nil || !strings.Contains(err.Error(), "created @MyShopBot (id 900)") {
		t.Fatalf("err = %v, want it to name the created bot", err)
	}
	if res.CreatedBot == nil || res.CreatedBot.ID != 900 {
		t.Errorf("created = %+v", res.CreatedBot)
	}
}

func TestPressCreateBotButtonRefusals(t *testing.T) {
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		return nil, errors.New("no RPC expected")
	})
	if _, err := pressCreateBot(t, api, createBotButton(false), pressOptions{}); err == nil {
		t.Error("answered an unmanaged create-bot request")
	}
	if _, err := pressCreateBot(t, api, createBotButton(true), pressOptions{botUsername: "bad-name"}); err == nil {
		t.Error("accepted an invalid username")
	}
	pick := &tg.KeyboardButtonRequestPeer{Text: "Pick", ButtonID: 1, PeerType: &tg.RequestPeerTypeUser{}}
	if _, err := pressCreateBot(t, api, pick, pressOptions{}); err == nil {
		t.Error("answered a request for an existing user")
	}
}

func TestNewBotAccessResult(t *testing.T) {
	s := &tg.BotsAccessSettings{Restricted: true}
	s.SetAddUsers([]tg.UserClass{&tg.User{ID: 5, FirstName: "Ann", Username: "ann"}, &tg.UserEmpty{ID: 6}})
	res := newBotAccessResult(peerRef{ID: 1, Type: peerUser, Username: "shop_bot"}, s)
	if !res.Restricted || len(res.Users) != 1 || res.Users[0].ID != 5 || res.Users[0].Username != "ann" {
		t.Errorf("result = %+v", res)
	}
	var b strings.Builder
	if err := res.MarshalText(&b); err != nil || !strings.Contains(b.String(), "restricted to its owner and") {
		t.Errorf("text = %q, %v", b.String(), err)
	}
}

func TestBotInfoManagedFields(t *testing.T) {
	full := &tg.UserFull{}
	full.SetBotManagerID(77)
	res := newBotInfoResult(peerRef{ID: 1}, &tg.User{ID: 1, Bot: true, BotCanManageBots: true}, full)
	if !res.CanManageBots || res.ManagerID != 77 {
		t.Errorf("result = %+v", res)
	}
}

func TestPeerCacheFileIsPerSessionKind(t *testing.T) {
	a := &app{configPath: filepath.Join(t.TempDir(), "gotd.cli.yaml")}
	user := &accountState{label: defaultAccount, acc: Account{BotToken: "1:x"}}
	bot := &accountState{label: defaultAccount, acc: Account{BotToken: "1:x"}, kind: authBot}
	u, b := a.peerCacheFile(user), a.peerCacheFile(bot)
	if u == b {
		t.Fatalf("user and bot sessions share the peer cache %s", u)
	}
	if !strings.Contains(filepath.Base(u), ".user.") || !strings.Contains(filepath.Base(b), ".bot.") {
		t.Errorf("paths = %s, %s", u, b)
	}
}
