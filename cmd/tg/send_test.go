package main

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

func TestPreviewOptionsValidate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		o         previewOptions
		noWebpage bool
		ok        bool
	}{
		{"none", previewOptions{}, false, true},
		{"https", previewOptions{url: "https://example.com/shop", large: true, above: true}, false, true},
		{"large without url", previewOptions{large: true}, false, false},
		{"above without url", previewOptions{above: true}, false, false},
		{"with nowebpage", previewOptions{url: "https://example.com"}, true, false},
		{"not http", previewOptions{url: "tg://resolve?domain=x"}, false, false},
		{"no host", previewOptions{url: "https://"}, false, false},
	} {
		err := tc.o.validate(tc.noWebpage)
		if (err == nil) != tc.ok {
			t.Errorf("%s: validate = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// sentUpdates answers a send with the new message's id.
func sentUpdates(id int) *tg.UpdateShortSentMessage {
	return &tg.UpdateShortSentMessage{ID: id}
}

func TestSendStyledWithPreview(t *testing.T) {
	var got *tg.MessagesSendMediaRequest
	previews := 0
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.MessagesGetWebPagePreviewRequest:
			previews++
			var page tg.WebPageClass = &tg.WebPagePending{ID: 1}
			if previews > 1 {
				page = &tg.WebPage{ID: 1, URL: r.Message, DisplayURL: "example.com/shop"}
			}
			return &tg.MessagesWebPagePreview{Media: &tg.MessageMediaWebPage{Webpage: page}}, nil
		case *tg.MessagesSendMediaRequest:
			got = r
			return sentUpdates(7), nil
		default:
			t.Fatalf("unexpected request %T", req)
			return nil, nil
		}
	})
	b := &message.NewSender(api).To(&tg.InputPeerSelf{}).Builder
	opts := []styling.StyledTextOption{html.String(nil, `<b>Shop</b> <a href="https://t.me/x_bot?start=g1">@x_bot</a>`)}
	id, err := sendStyled(context.Background(), api, b, opts, previewOptions{url: "https://example.com/shop", large: true, above: true})
	if err != nil || id != 7 {
		t.Fatalf("sendStyled = %d, %v", id, err)
	}
	if previews != 2 {
		t.Fatalf("preview requests = %d, want 2 (pending, then ready)", previews)
	}
	wp, ok := got.Media.(*tg.InputMediaWebPage)
	if !ok || wp.URL != "https://example.com/shop" || !wp.ForceLargeMedia || !wp.Optional {
		t.Fatalf("media = %#v", got.Media)
	}
	if !got.InvertMedia {
		t.Fatal("preview not above the text")
	}
	if got.Message != "Shop @x_bot" {
		t.Fatalf("message = %q", got.Message)
	}
	var link *tg.MessageEntityTextURL
	for _, e := range got.Entities {
		if l, ok := e.(*tg.MessageEntityTextURL); ok {
			link = l
		}
	}
	if link == nil || link.URL != "https://t.me/x_bot?start=g1" || link.Offset != 5 || link.Length != 6 {
		t.Fatalf("hidden link entity = %#v (all: %#v)", link, got.Entities)
	}
}

func TestSendStyledPlain(t *testing.T) {
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		r, ok := req.(*tg.MessagesSendMessageRequest)
		if !ok {
			t.Fatalf("unexpected request %T", req)
		}
		if r.Message != "hi" || r.InvertMedia {
			t.Fatalf("request = %#v", r)
		}
		return sentUpdates(3), nil
	})
	b := &message.NewSender(api).To(&tg.InputPeerSelf{}).Builder
	id, err := sendStyled(context.Background(), api, b, []styling.StyledTextOption{styling.Plain("hi")}, previewOptions{})
	if err != nil || id != 3 {
		t.Fatalf("sendStyled = %d, %v", id, err)
	}
}
