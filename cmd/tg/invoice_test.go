package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-faster/errors"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/gotd/cli/internal/peercache"
)

func TestParseInvoiceSlug(t *testing.T) {
	for _, tc := range []struct {
		arg, slug string
		ok        bool
	}{
		{"https://t.me/$AbC-12_x", "AbC-12_x", true},
		{"http://t.me/$AbC", "AbC", true},
		{"t.me/$AbC", "AbC", true},
		{"https://telegram.me/$AbC", "AbC", true},
		{"https://t.me/invoice/AbC", "AbC", true},
		{"t.me/invoice/AbC?x=1", "AbC", true},
		{"tg://invoice?slug=AbC", "AbC", true},
		{"$AbC", "AbC", true},
		{"  https://t.me/$AbC  ", "AbC", true},
		{"https://t.me/$", "", false},
		{"https://t.me/invoice/", "", false},
		{"https://t.me/$AbC/extra", "", false},
		{"https://t.me/invoice/AbC#frag", "AbC", true},
		{"$AbC/extra", "", false},
		{"$AbC?x=1", "", false},
		{"$./..", "", false},
		{"$Ab C", "", false},
		{"tg://invoice?slug=AbC%2FDEF", "", false},
		{"tg://invoice?slug=AbC%23x", "", false},
		{"https://t.me/mybot", "", false},
		{"https://example.com/$AbC", "", false},
		{"tg://resolve?domain=mybot", "", false},
		{"tg://invoice", "", false},
		{"@mybot", "", false},
		{"mybot", "", false},
	} {
		slug, ok := parseInvoiceSlug(tc.arg)
		if slug != tc.slug || ok != tc.ok {
			t.Errorf("parseInvoiceSlug(%q) = %q, %v; want %q, %v", tc.arg, slug, ok, tc.slug, tc.ok)
		}
	}
}

func starsForm() *tg.PaymentsPaymentFormStars {
	return &tg.PaymentsPaymentFormStars{
		FormID:      77,
		BotID:       9,
		Title:       "Order #12",
		Description: "2 items",
		Invoice: tg.Invoice{
			Currency: "XTR",
			Prices:   []tg.LabeledPrice{{Label: "Guide", Amount: 10}, {Label: "Template", Amount: 5}},
		},
		Users: []tg.UserClass{
			&tg.User{ID: 3, Username: "someone"},
			&tg.User{ID: 9, Username: "shopbot", FirstName: "Shop", LastName: "Bot"},
		},
	}
}

func TestNewInvoiceResultStars(t *testing.T) {
	res, err := newInvoiceResult(starsForm())
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != invoiceKindStars || res.FormID != 77 || res.Currency != "XTR" {
		t.Errorf("kind %q form %d currency %q", res.Kind, res.FormID, res.Currency)
	}
	if res.Title != "Order #12" || res.Description != "2 items" {
		t.Errorf("title %q description %q", res.Title, res.Description)
	}
	if res.Total != 15 || len(res.Prices) != 2 || res.Prices[1] != (invoicePrice{Label: "Template", Amount: 5}) {
		t.Errorf("total %d prices %+v", res.Total, res.Prices)
	}
	if res.Bot == nil || res.Bot.ID != 9 || res.Bot.Username != "shopbot" || res.Bot.Name != "Shop Bot" {
		t.Errorf("bot %+v", res.Bot)
	}
}

func TestNewInvoiceResultProviderAndGift(t *testing.T) {
	res, err := newInvoiceResult(&tg.PaymentsPaymentForm{
		FormID: 5, BotID: 4, Title: "Plan",
		Invoice: tg.Invoice{Currency: "USD", Test: true, Prices: []tg.LabeledPrice{{Label: "Plan", Amount: 1999}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != invoiceKindPayment || res.Total != 1999 || !res.Test || res.Bot == nil || res.Bot.ID != 4 {
		t.Errorf("provider form: %+v", res)
	}

	res, err = newInvoiceResult(&tg.PaymentsPaymentFormStarGift{
		FormID: 6, Invoice: tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: "Gift", Amount: 50}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != invoiceKindStarGift || res.Total != 50 || res.Bot != nil {
		t.Errorf("gift form: %+v", res)
	}
}

func TestNewInvoiceResultNoPricesIsAnEmptyList(t *testing.T) {
	res, err := newInvoiceResult(&tg.PaymentsPaymentFormStars{FormID: 1, Invoice: tg.Invoice{Currency: "XTR"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"prices":[]`) {
		t.Errorf("prices should encode as an empty list: %s", data)
	}
}

func TestGetInvoiceFormAsksForTheSlug(t *testing.T) {
	var got tg.InputInvoiceClass
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		if r, ok := req.(*tg.PaymentsGetPaymentFormRequest); ok {
			got = r.Invoice
			return starsForm(), nil
		}
		return nil, errors.Errorf("unexpected request %T", req)
	})

	res, err := getInvoiceForm(context.Background(), api, &tg.InputInvoiceSlug{Slug: "AbC"})
	if err != nil {
		t.Fatal(err)
	}
	if slug, ok := got.(*tg.InputInvoiceSlug); !ok || slug.Slug != "AbC" {
		t.Errorf("invoice = %#v, want the slug AbC", got)
	}
	if res.Total != 15 {
		t.Errorf("total %d", res.Total)
	}
}

func TestGetInvoiceFormNamesTheCallOnError(t *testing.T) {
	api := newFuncAPI(t, func(bin.Encoder) (bin.Encoder, error) {
		return nil, errors.New("INVOICE_INVALID")
	})
	_, err := getInvoiceForm(context.Background(), api, &tg.InputInvoiceSlug{Slug: "nope"})
	if err == nil || !strings.Contains(err.Error(), "payments.getPaymentForm") {
		t.Errorf("err = %v, want it to name payments.getPaymentForm", err)
	}
}

func TestInvoiceResultText(t *testing.T) {
	res, err := newInvoiceResult(starsForm())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := res.MarshalText(&buf); err != nil {
		t.Fatal(err)
	}
	want := "invoice \"Order #12\" from @shopbot (stars, form 77)\n2 items\n  Guide: 10 XTR\n  Template: 5 XTR\ntotal: 15 XTR\n"
	if buf.String() != want {
		t.Errorf("text =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestNewInvoiceResultRefusesAnUnknownForm(t *testing.T) {
	if _, err := newInvoiceResult(nil); err == nil {
		t.Error("an unknown form should be an error")
	}
}

func TestReadInvoiceMessageAsksOnlyForTheForm(t *testing.T) {
	store, err := peercache.Open(filepath.Join(t.TempDir(), "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got tg.InputInvoiceClass
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.UsersGetUsersRequest:
			return &tg.UserClassVector{Elems: []tg.UserClass{&tg.User{ID: 42, Self: true, AccessHash: 1}}}, nil
		case *tg.PaymentsGetPaymentFormRequest:
			got = r.Invoice
			return starsForm(), nil
		}
		// Anything else, a call that pays included, fails the test.
		return nil, errors.Errorf("unexpected request %T", req)
	})
	m := &peerManager{Manager: peers.Options{Storage: store}.Build(api), store: store}

	// "me" is a documented <peer>; the username resolver alone refuses it.
	res, err := readInvoiceMessage(context.Background(), api, m, "me", 4270)
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := got.(*tg.InputInvoiceMessage)
	if !ok || msg.MsgID != 4270 {
		t.Fatalf("invoice = %#v, want message 4270", got)
	}
	if _, ok := msg.Peer.(*tg.InputPeerSelf); !ok {
		t.Errorf("peer = %#v, want this account", msg.Peer)
	}
	if res.Peer == nil || res.Peer.ID != 42 || res.MessageID != 4270 || res.Total != 15 {
		t.Errorf("result %+v", res)
	}
}
