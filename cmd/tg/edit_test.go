package main

import (
	"context"
	"testing"

	"github.com/go-faster/errors"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// TestEditMessageLinkedChannel covers an edit of a channel post whose discussion
// group holds a copy: Telegram answers with two edit updates, the group's first.
func TestEditMessageLinkedChannel(t *testing.T) {
	edited := func(channel int64, id int) tg.UpdateClass {
		return &tg.UpdateEditChannelMessage{Message: &tg.Message{
			ID: id, Message: "new", PeerID: &tg.PeerChannel{ChannelID: channel},
		}}
	}
	var req *tg.MessagesEditMessageRequest
	api := newFuncAPI(t, func(r bin.Encoder) (bin.Encoder, error) {
		e, ok := r.(*tg.MessagesEditMessageRequest)
		if !ok {
			return nil, errors.Errorf("unexpected request %T", r)
		}
		req = e
		return &tg.Updates{Updates: []tg.UpdateClass{edited(2, 95), edited(1, 21)}}, nil
	})

	b := message.NewSender(api).To(&tg.InputPeerSelf{})
	if err := editMessage(context.Background(), b, 21, styling.Plain("new")); err != nil {
		t.Fatal(err)
	}
	if req.ID != 21 || req.Message != "new" {
		t.Errorf("request = id %d text %q", req.ID, req.Message)
	}
}

func TestEditMessageError(t *testing.T) {
	api := newFuncAPI(t, func(bin.Encoder) (bin.Encoder, error) {
		return nil, errors.New("MESSAGE_NOT_MODIFIED")
	})
	b := message.NewSender(api).To(&tg.InputPeerSelf{})
	if err := editMessage(context.Background(), b, 21, styling.Plain("same")); err == nil {
		t.Fatal("edit error was swallowed")
	}
}
