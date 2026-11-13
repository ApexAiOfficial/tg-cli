package main

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// historyServer answers messages.getHistory like Telegram for a chat whose
// message ids run 1..n: offset_id itself is excluded, add_offset moves the
// window towards newer messages when negative.
func historyServer(t *testing.T, n int) *tg.Client {
	return newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		r, ok := req.(*tg.MessagesGetHistoryRequest)
		if !ok {
			t.Fatalf("unexpected request %T", req)
		}
		// Newest first; the first returned id is offset_id-1-add_offset.
		var msgs []tg.MessageClass
		for id := r.OffsetID - 1 - r.AddOffset; id >= 1 && len(msgs) < r.Limit; id-- {
			if id <= n {
				msgs = append(msgs, &tg.Message{ID: id, PeerID: &tg.PeerUser{UserID: 1}})
			}
		}
		return &tg.MessagesMessages{Messages: msgs}, nil
	})
}

func TestMessageContextCentersOnTheMessage(t *testing.T) {
	for _, tc := range []struct {
		radius int
		want   []int
	}{
		{0, []int{50}},
		{1, []int{49, 50, 51}},
		{2, []int{48, 49, 50, 51, 52}},
	} {
		res, err := messageContext(context.Background(), historyServer(t, 100), &tg.InputPeerSelf{}, 50, tc.radius)
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		for _, m := range res.Messages {
			got = append(got, m.ID)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("radius %d: ids %v, want %v", tc.radius, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("radius %d: ids %v, want %v", tc.radius, got, tc.want)
			}
		}
	}
}
