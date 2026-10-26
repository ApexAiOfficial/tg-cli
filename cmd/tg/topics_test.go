package main

import (
	"context"
	"testing"

	"github.com/go-faster/errors"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
)

func TestParseTopicColor(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "6FB9F0", want: 0x6FB9F0},
		{in: "#ffd67e", want: 0xFFD67E},
		{in: "0xCB86DB", want: 0xCB86DB},
		{in: "7322096", want: 0x6FB9F0},
		{in: "123456", wantErr: true},
		{in: "red", wantErr: true},
		{in: "", wantErr: true},
	} {
		got, err := parseTopicColor(tc.in)
		if tc.wantErr {
			var u usageError
			if !errors.As(err, &u) {
				t.Errorf("parseTopicColor(%q) err = %v, want usage error", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseTopicColor(%q) = %#x, %v; want %#x", tc.in, got, err, tc.want)
		}
	}
}

func TestParseTopicIDs(t *testing.T) {
	ids, err := parseTopicIDs([]string{"1", "54"})
	if err != nil || len(ids) != 2 || ids[0] != 1 || ids[1] != 54 {
		t.Fatalf("parseTopicIDs = %v, %v", ids, err)
	}
	for _, bad := range []string{"0", "-3", "x"} {
		if _, err := parseTopicIDs([]string{bad}); err == nil {
			t.Errorf("parseTopicIDs(%q) accepted", bad)
		}
	}
}

func TestCreatedTopicID(t *testing.T) {
	created := &tg.MessageService{ID: 77, Action: &tg.MessageActionTopicCreate{Title: "Ideas"}}
	for _, tc := range []struct {
		name   string
		in     tg.UpdatesClass
		want   int
		wantOK bool
	}{
		{
			name: "updates",
			in: &tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateMessageID{ID: 77},
				&tg.UpdateNewChannelMessage{Message: created},
			}},
			want: 77, wantOK: true,
		},
		{
			name:   "combined",
			in:     &tg.UpdatesCombined{Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: created}}},
			want:   77,
			wantOK: true,
		},
		{
			name: "other service message",
			in: &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{
				Message: &tg.MessageService{ID: 5, Action: &tg.MessageActionPinMessage{}},
			}}},
		},
		{name: "empty", in: &tg.UpdatesTooLong{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := createdTopicID(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("createdTopicID = %d, %v; want %d, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestTopicEditRequest(t *testing.T) {
	changed := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, c := range names {
				if c == n {
					return true
				}
			}
			return false
		}
	}

	req, err := topicEdit{title: "Help", closed: true}.request(12, changed("title", "closed"))
	if err != nil {
		t.Fatal(err)
	}
	if title, ok := req.GetTitle(); !ok || title != "Help" {
		t.Errorf("title = %q, %v", title, ok)
	}
	if closed, ok := req.GetClosed(); !ok || !closed {
		t.Errorf("closed = %v, %v", closed, ok)
	}
	if _, ok := req.GetIconEmojiID(); ok {
		t.Error("icon set although not given")
	}
	if _, ok := req.GetHidden(); ok {
		t.Error("hidden set although not given")
	}

	req, err = topicEdit{emojiID: "0"}.request(12, changed("icon-emoji-id"))
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := req.GetIconEmojiID(); !ok || id != 0 {
		t.Errorf("clearing icon: id = %d, %v", id, ok)
	}

	if _, err := (topicEdit{hidden: true}).request(12, changed("hidden")); err == nil {
		t.Error("hiding a topic other than General was accepted")
	}
	req, err = topicEdit{hidden: true}.request(generalTopicID, changed("hidden"))
	if err != nil {
		t.Fatal(err)
	}
	if hidden, ok := req.GetHidden(); !ok || !hidden {
		t.Errorf("hidden = %v, %v", hidden, ok)
	}

	if _, err := (topicEdit{}).request(12, changed()); err == nil {
		t.Error("edit with no changes was accepted")
	}
}

func TestTopicIconsFromSet(t *testing.T) {
	set := &tg.MessagesStickerSet{Documents: []tg.DocumentClass{
		&tg.Document{ID: 5312536423851630001, Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeCustomEmoji{Alt: "💡"},
		}},
		&tg.DocumentEmpty{ID: 1},
	}}
	icons := topicIconsFromSet(set)
	if len(icons) != 1 || icons[0].Emoji != "💡" || icons[0].DocumentID != "5312536423851630001" {
		t.Fatalf("icons = %+v", icons)
	}
	if got := topicIconsFromSet(&tg.MessagesStickerSetNotModified{}); got != nil {
		t.Errorf("not-modified set = %+v", got)
	}
}

func TestNewTopicItem(t *testing.T) {
	item := newTopicItem(&tg.ForumTopic{
		ID: 12, Title: "Help", IconColor: 0x6FB9F0, IconEmojiID: 5312536423851630001,
		Closed: true, Pinned: true, TopMessage: 40, UnreadCount: 3, Date: 1700000000,
	})
	if item.IconEmojiID != "5312536423851630001" || !item.Closed || !item.Pinned || item.TopMessage != 40 {
		t.Errorf("item = %+v", item)
	}
	if got := item.topicFlags(); got != " [pinned,closed]" {
		t.Errorf("flags = %q", got)
	}
	if got := newTopicItem(&tg.ForumTopic{ID: 1, Title: "General"}).IconEmojiID; got != "" {
		t.Errorf("empty icon rendered as %q", got)
	}
}

func TestListTopicsPaginates(t *testing.T) {
	page := func(from, n, count int) *tg.MessagesForumTopics {
		res := &tg.MessagesForumTopics{Count: count}
		for i := 0; i < n; i++ {
			id := from + i
			res.Topics = append(res.Topics, &tg.ForumTopic{ID: id, Title: "t", TopMessage: id * 10, Date: id, Peer: &tg.PeerChannel{ChannelID: 1}, FromID: &tg.PeerUser{UserID: 1}})
			res.Messages = append(res.Messages, &tg.Message{ID: id * 10, Date: 1000 + id, PeerID: &tg.PeerChannel{ChannelID: 1}})
		}
		return res
	}
	var reqs []*tg.MessagesGetForumTopicsRequest
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		r, ok := req.(*tg.MessagesGetForumTopicsRequest)
		if !ok {
			return nil, errors.Errorf("unexpected request %T", req)
		}
		reqs = append(reqs, r)
		if len(reqs) == 1 {
			return page(1, topicsPageSize, 150), nil
		}
		return page(1+topicsPageSize, 50, 150), nil
	})

	topics, err := listTopics(context.Background(), api, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 150 {
		t.Fatalf("got %d topics, want 150", len(topics))
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	last := topicsPageSize
	if r := reqs[1]; r.OffsetTopic != last || r.OffsetID != last*10 || r.OffsetDate != 1000+last {
		t.Errorf("second page offsets = topic %d id %d date %d", r.OffsetTopic, r.OffsetID, r.OffsetDate)
	}
}

func TestDeleteTopicFollowsOffset(t *testing.T) {
	var calls int
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		r, ok := req.(*tg.MessagesDeleteTopicHistoryRequest)
		if !ok {
			return nil, errors.Errorf("unexpected request %T", req)
		}
		if r.TopMsgID != 54 {
			return nil, errors.Errorf("top msg id = %d", r.TopMsgID)
		}
		calls++
		offset := 0
		if calls == 1 {
			offset = 7
		}
		return &tg.MessagesAffectedHistory{Offset: offset}, nil
	})
	if err := deleteTopic(context.Background(), api, &tg.InputPeerSelf{}, 54); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestMessageOptionsTopic(t *testing.T) {
	for _, tc := range []struct {
		topic     int
		wantReply int
	}{{topic: 0}, {topic: generalTopicID}, {topic: 54, wantReply: 54}} {
		var sent *tg.MessagesSendMessageRequest
		api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
			r, ok := req.(*tg.MessagesSendMessageRequest)
			if !ok {
				return nil, errors.Errorf("unexpected request %T", req)
			}
			sent = r
			return &tg.UpdateShortSentMessage{ID: 1}, nil
		})
		o := messageOptions{topic: tc.topic, silent: true}
		b, opts := o.apply(message.NewSender(api).To(&tg.InputPeerSelf{}), "hi")
		if _, err := b.StyledText(context.Background(), opts...); err != nil {
			t.Fatal(err)
		}
		reply, _ := sent.ReplyTo.(*tg.InputReplyToMessage)
		switch {
		case tc.wantReply == 0 && reply != nil:
			t.Errorf("topic %d: reply_to = %+v, want none", tc.topic, reply)
		case tc.wantReply != 0 && (reply == nil || reply.ReplyToMsgID != tc.wantReply):
			t.Errorf("topic %d: reply_to = %+v, want %d", tc.topic, reply, tc.wantReply)
		}
		if !sent.Silent {
			t.Errorf("topic %d: --silent lost", tc.topic)
		}
	}
}
