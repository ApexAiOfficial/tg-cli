package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/go-faster/errors"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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
	id, err := editMessage(context.Background(), b, 21, styling.Plain("new"))
	if err != nil {
		t.Fatal(err)
	}
	if id != 21 {
		t.Errorf("reported id %d, want the channel post 21", id)
	}
	if req.ID != 21 || req.Message != "new" {
		t.Errorf("request = id %d text %q", req.ID, req.Message)
	}
}

func TestEditMessageErrors(t *testing.T) {
	for _, tc := range []struct {
		rpc     string
		wantErr bool
	}{
		{rpc: tg.ErrMessageNotModified},
		{rpc: tg.ErrMessageIDInvalid, wantErr: true},
	} {
		api := newFuncAPI(t, func(bin.Encoder) (bin.Encoder, error) {
			return nil, tgerr.New(400, tc.rpc)
		})
		b := message.NewSender(api).To(&tg.InputPeerSelf{})
		id, err := editMessage(context.Background(), b, 21, styling.Plain("same"))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error %v", tc.rpc, err, tc.wantErr)
		}
		if !tc.wantErr && id != 21 {
			t.Errorf("%s: id = %d", tc.rpc, id)
		}
	}
}

func TestEditedResultText(t *testing.T) {
	var buf bytes.Buffer
	if err := (editedResult{Peer: "@x", MessageID: 21}).MarshalText(&buf); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "edited message #21\n" {
		t.Errorf("text = %q", got)
	}
}
