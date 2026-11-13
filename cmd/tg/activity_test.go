package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

func TestCollectURLs(t *testing.T) {
	text := "Visit example.org and www.example.com or t.me/demo_bot now. Docs: https://docs.example.net/a."
	msg := &tg.Message{Message: text}
	msg.SetEntities([]tg.MessageEntityClass{
		&tg.MessageEntityURL{Offset: strings.Index(text, "https://"), Length: len("https://docs.example.net/a")},
		&tg.MessageEntityTextURL{Offset: 0, Length: 5, URL: "https://hidden.example/x"},
	})
	msg.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonURL{Text: "Open", URL: "https://button.example"},
		&tg.KeyboardButtonCallback{Text: "cb", Data: []byte("x")},
	}}}})
	got := collectURLs(msg)
	want := []string{"https://docs.example.net/a", "https://hidden.example/x", "www.example.com", "t.me/demo_bot", "https://button.example"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("collectURLs = %v\nwant %v", got, want)
	}
}

func TestLinksToBot(t *testing.T) {
	for _, tc := range []struct {
		m    activityMsg
		want bool
	}{
		{activityMsg{URLs: []string{"https://t.me/SomeDemoBot?start=x"}}, true},
		{activityMsg{URLs: []string{"t.me/some_channel"}}, false},
		{activityMsg{URLs: []string{"https://example.com/bot"}}, false},
		{activityMsg{Mentions: []string{"helper_bot"}}, true},
		{activityMsg{Mentions: []string{"someone"}}, false},
	} {
		if got := linksToBot(tc.m); got != tc.want {
			t.Errorf("linksToBot(%+v) = %v, want %v", tc.m, got, tc.want)
		}
	}
}

func TestNewActivityMsg(t *testing.T) {
	ent := entitiesOf([]tg.UserClass{&tg.User{ID: 5, Bot: true}}, nil)
	msg := &tg.Message{ID: 1, Date: 100, Message: "ask @helper_bot", PeerID: &tg.PeerChannel{ChannelID: 9}}
	msg.SetFromID(&tg.PeerUser{UserID: 5})
	msg.SetFwdFrom(tg.MessageFwdHeader{})
	msg.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: 1})
	msg.SetMedia(&tg.MessageMediaPhoto{})
	am, ok := newActivityMsg(msg, ent)
	if !ok || am.SenderID != 5 || !am.SenderBot || !am.Forward || !am.Reply || !am.Media {
		t.Fatalf("newActivityMsg = %+v, %v", am, ok)
	}
	if len(am.Mentions) != 1 || am.Mentions[0] != "helper_bot" {
		t.Fatalf("mentions = %v", am.Mentions)
	}

	post := &tg.Message{ID: 2, Date: 200, Post: true, PeerID: &tg.PeerChannel{ChannelID: 9}, Views: 40}
	am, _ = newActivityMsg(post, peer.Entities{})
	if am.SenderID != -9 || am.Views != 40 {
		t.Fatalf("channel post = %+v", am)
	}

	svc := &tg.MessageService{ID: 3, Date: 300, Action: &tg.MessageActionChatJoinedByLink{}}
	am, ok = newActivityMsg(svc, peer.Entities{})
	if !ok || am.Service != "chat_joined_by_link" {
		t.Fatalf("service = %+v, %v", am, ok)
	}
}

func TestComputeActivity(t *testing.T) {
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC) // a Monday
	at := func(h float64) int { return int(base.Add(time.Duration(h * float64(time.Hour))).Unix()) }
	promo := "Great new product for every seller, check it out today"
	msgs := []activityMsg{
		{Date: at(0), SenderID: 1, Text: promo, URLs: []string{"https://t.me/ShopOneBot"}},
		{Date: at(1), SenderID: 2, Text: "hello", Reply: true},
		{Date: at(2), SenderID: 3, Text: "photo", Media: true, Forward: true},
		{Date: at(24), SenderID: 1, Text: promo + " 2", URLs: []string{"https://example.com/x"}}, // duplicate once digits are stripped
		{Date: at(30), SenderID: 4, SenderBot: true, Text: "welcome @newcomer", Mentions: []string{"newcomer"}},
		{Date: at(48), SenderID: 1, Text: "third", URLs: []string{"www.example.com"}},
		{Date: at(10), Service: actionChatAddUser},
		{Date: at(11), Service: "pin_message"},
	}
	now := base.Add(50 * time.Hour)
	res := computeActivity(peerRef{ID: 9, Type: peerChannel, Username: "g"}, msgs, now, activityWindow{Exhausted: true})

	if res.Sampled != 6 || res.Service != 2 || res.Joins != 1 {
		t.Fatalf("counts: sampled %d service %d joins %d", res.Sampled, res.Service, res.Joins)
	}
	if res.SpanHours != 48 || res.PerDay != 2.9 || res.ActiveDays != 3 || res.LastAgeHours != 2 {
		t.Fatalf("span %v perday %v days %d age %v", res.SpanHours, res.PerDay, res.ActiveDays, res.LastAgeHours)
	}
	if res.Senders != 4 || res.TopSenderShare != 1 {
		t.Fatalf("senders %d top %v", res.Senders, res.TopSenderShare)
	}
	if res.LinkShare != 0.5 || res.BotLinkShare != 0.167 || res.MentionShare != 0.167 || res.ForwardShare != 0.167 || res.MediaShare != 0.167 ||
		res.ReplyShare != 0.167 || res.BotShare != 0.167 || res.DuplicateShare != 0.167 {
		t.Fatalf("shares wrong: %+v", res)
	}
	if res.ByHour[9] != 3 || res.ByWeekday[1] != 3 || res.PeakHours[0] != 9 {
		t.Fatalf("histograms wrong: hour9=%d mon=%d peaks=%v", res.ByHour[9], res.ByWeekday[1], res.PeakHours)
	}
	if res.TopDomains[0] != (countItem{Name: "example.com", Count: 2}) {
		t.Fatalf("top domains = %v", res.TopDomains)
	}
	if p := res.Promoters; p.Senders != 1 || p.Repeat != 1 || p.MinGapHours != 24 || p.MedianGapHours != 24 {
		t.Fatalf("promoters = %+v", p)
	}
}

func TestComputeActivityEmpty(t *testing.T) {
	res := computeActivity(peerRef{ID: 1, Type: peerChannel}, []activityMsg{{Date: 5, Service: actionChatAddUser}}, time.Unix(10, 0), activityWindow{Exhausted: true})
	if res.Sampled != 0 || res.Joins != 1 || res.PeakHours != nil {
		t.Fatalf("empty = %+v", res)
	}
	var buf bytes.Buffer
	if err := res.MarshalText(&buf); err != nil || !strings.Contains(buf.String(), "0 messages") {
		t.Fatalf("text = %q, %v", buf.String(), err)
	}
}

func TestActivityText(t *testing.T) {
	res := activityResult{
		Peer: peerRef{Type: peerChannel, Username: "g"}, Sampled: 10, SpanHours: 20, PerDay: 10, ActiveDays: 1, LastAgeHours: 1,
		Senders: 4, PeakHours: []int{9, 17}, Promoters: promoterStats{Senders: 2, Repeat: 1, MedianGapHours: 24, MinGapHours: 24},
		TopDomains: []countItem{{Name: "t.me", Count: 3}},
	}
	var buf bytes.Buffer
	if err := res.MarshalText(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@g: 10 messages over 20.0 h", "peak hours (UTC): 09:00, 17:00", "median gap 24.0 h", "top domains: t.me 3"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestPeakHoursTies(t *testing.T) {
	var h [24]int
	h[3], h[5], h[20] = 2, 2, 7
	if got := peakHours(h, 2); len(got) != 2 || got[0] != 20 || got[1] != 3 {
		t.Fatalf("peakHours = %v", got)
	}
}

func TestPerDay(t *testing.T) {
	const day = 86400
	for _, tc := range []struct {
		name       string
		n          float64
		span       int
		sinceFirst int
		window     activityWindow
		want       float64
	}{
		{"truncated busy chat: rate over the span", 400, 4 * 3600, 4 * 3600, activityWindow{Truncated: true}, 2400},
		{"truncated burst: span floored at an hour", 50, 60, 60, activityWindow{Truncated: true}, 1200},
		{"window reached: rate over the window", 42, 3600, 20 * day, activityWindow{Days: 21}, 2},
		{"history starts inside the window", 30, 9 * day, 10 * day, activityWindow{Days: 21, Exhausted: true}, 3},
		{"young chat: at least a day", 3, 600, 600, activityWindow{Days: 21, Exhausted: true}, 3},
		{"no window: since the first message", 30, 9 * day, 10 * day, activityWindow{Exhausted: true}, 3},
	} {
		if got := perDay(tc.n, tc.span, tc.sinceFirst, tc.window); got != tc.want {
			t.Errorf("%s: perDay = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsRealReply(t *testing.T) {
	withHeader := func(h tg.MessageReplyHeader) *tg.Message {
		m := &tg.Message{}
		m.SetReplyTo(&h)
		return m
	}
	topicMsg := tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 10}
	topicReply := tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 12}
	topicReply.SetReplyToTopID(10)
	for _, tc := range []struct {
		name string
		m    *tg.Message
		want bool
	}{
		{"no header", &tg.Message{}, false},
		{"plain reply", withHeader(tg.MessageReplyHeader{ReplyToMsgID: 3}), true},
		{"message in a forum topic", withHeader(topicMsg), false},
		{"reply inside a forum topic", withHeader(topicReply), true},
	} {
		if got := isRealReply(tc.m); got != tc.want {
			t.Errorf("%s: isRealReply = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// historyAPI serves a chat whose messages have ids 1..n and date 100*id,
// honoring the history request's offset and limit.
func historyAPI(t *testing.T, n int) *tg.Client {
	return newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		r, ok := req.(*tg.MessagesGetHistoryRequest)
		if !ok {
			t.Fatalf("unexpected request %T", req)
		}
		top := n
		if r.OffsetID > 0 {
			top = r.OffsetID - 1
		}
		var msgs []tg.MessageClass
		for id := top; id >= 1 && len(msgs) < r.Limit; id-- {
			msgs = append(msgs, &tg.Message{ID: id, Date: 100 * id, Message: "m", PeerID: &tg.PeerChannel{ChannelID: 1}})
		}
		return &tg.MessagesChannelMessages{Messages: msgs, Count: n}, nil
	})
}

func TestReadActivityStops(t *testing.T) {
	ctx := context.Background()
	ip := &tg.InputPeerChannel{ChannelID: 1}

	msgs, w, err := readActivity(ctx, historyAPI(t, 5), ip, 3, 0)
	if err != nil || len(msgs) != 3 || !w.Truncated || w.Exhausted {
		t.Fatalf("limit: %d msgs, %+v, %v", len(msgs), w, err)
	}
	msgs, w, err = readActivity(ctx, historyAPI(t, 5), ip, 10, 250)
	if err != nil || len(msgs) != 3 || w.Truncated || w.Exhausted {
		t.Fatalf("cutoff: %d msgs, %+v, %v", len(msgs), w, err)
	}
	msgs, w, err = readActivity(ctx, historyAPI(t, 5), ip, 10, 0)
	if err != nil || len(msgs) != 5 || w.Truncated || !w.Exhausted {
		t.Fatalf("whole history: %d msgs, %+v, %v", len(msgs), w, err)
	}
	msgs, w, err = readActivity(ctx, historyAPI(t, 2), ip, 2, 0)
	if err != nil || len(msgs) != 2 || w.Truncated || !w.Exhausted {
		t.Fatalf("limit equal to history: %d msgs, %+v, %v", len(msgs), w, err)
	}
}
