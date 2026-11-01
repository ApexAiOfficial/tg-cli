package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/tg"
)

// What an invoice's payment form is for.
const (
	invoiceKindStars    = "stars"     // a bot's invoice in Telegram Stars (XTR)
	invoiceKindPayment  = "payment"   // a bot's invoice through a payment provider
	invoiceKindStarGift = "star_gift" // a Star gift
)

// invoicePrice is one line of an invoice. Amount is in the currency's smallest
// unit: whole Stars for XTR, cents for USD.
type invoicePrice struct {
	Label  string `json:"label"`
	Amount int64  `json:"amount"`
}

// invoiceResult is the result of `tg invoice`: the payment form Telegram shows
// for an invoice, read without paying it.
type invoiceResult struct {
	// Slug is the invoice link's slug; Peer and MessageID name an invoice message.
	Slug      string   `json:"slug,omitempty"`
	Peer      *peerRef `json:"peer,omitempty"`
	MessageID int      `json:"message_id,omitempty"`
	// Kind is "stars", "payment" or "star_gift".
	Kind        string         `json:"kind"`
	FormID      int64          `json:"form_id"`
	Bot         *peerRef       `json:"bot,omitempty"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Currency    string         `json:"currency"`
	Prices      []invoicePrice `json:"prices"`
	// Total is the sum of Prices, in the same unit.
	Total int64 `json:"total"`
	// Test is set on an invoice from Telegram's test environment.
	Test bool `json:"test,omitempty"`
	// SubscriptionPeriod is the period of a subscription invoice, in seconds.
	SubscriptionPeriod int    `json:"subscription_period,omitempty"`
	TermsURL           string `json:"terms_url,omitempty"`
}

// MarshalText renders the form the way a payment sheet summarizes it.
func (r invoiceResult) MarshalText(w io.Writer) error {
	head := "invoice"
	if r.Title != "" {
		head = fmt.Sprintf("invoice %q", r.Title)
	}
	if r.Bot != nil {
		head += " from " + r.Bot.label()
	}
	head += fmt.Sprintf(" (%s, form %d)", r.Kind, r.FormID)
	lines := []string{head}
	if r.Description != "" {
		lines = append(lines, r.Description)
	}
	for _, p := range r.Prices {
		lines = append(lines, fmt.Sprintf("  %s: %d %s", p.Label, p.Amount, r.Currency))
	}
	lines = append(lines, fmt.Sprintf("total: %d %s", r.Total, r.Currency))
	if r.SubscriptionPeriod != 0 {
		lines = append(lines, fmt.Sprintf("subscription: every %d seconds", r.SubscriptionPeriod))
	}
	if r.Test {
		lines = append(lines, "test invoice")
	}
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}

// parseInvoiceSlug returns the slug of an invoice link: t.me/$<slug>,
// t.me/invoice/<slug> (with or without https://, or on telegram.me),
// tg://invoice?slug=<slug>, or a bare $<slug>. ok is false for anything else.
func parseInvoiceSlug(arg string) (slug string, ok bool) {
	arg = strings.TrimSpace(arg)
	if s, found := strings.CutPrefix(arg, "$"); found {
		return s, s != ""
	}
	if strings.HasPrefix(arg, "tg:") {
		u, err := url.Parse(arg)
		if err != nil || u.Host != "invoice" {
			return "", false
		}
		s := u.Query().Get("slug")
		return s, s != ""
	}
	rest := arg
	for _, scheme := range []string{"https://", "http://"} {
		if r, found := strings.CutPrefix(rest, scheme); found {
			rest = r
			break
		}
	}
	host, path, found := strings.Cut(rest, "/")
	if !found || (host != "t.me" && host != "telegram.me") {
		return "", false
	}
	path, _, _ = strings.Cut(path, "?")
	s, found := strings.CutPrefix(path, "$")
	if !found {
		s, found = strings.CutPrefix(path, "invoice/")
	}
	if !found || s == "" || strings.Contains(s, "/") {
		return "", false
	}
	return s, true
}

// botRef finds the bot that issued a form among the form's users.
func botRef(botID int64, users []tg.UserClass) *peerRef {
	if botID == 0 {
		return nil
	}
	ref := &peerRef{ID: botID, Type: peerUser}
	for _, u := range users {
		if user, ok := u.(*tg.User); ok && user.ID == botID {
			ref.Name = strings.TrimSpace(user.FirstName + " " + user.LastName)
			ref.Username = user.Username
		}
	}
	return ref
}

// newInvoiceResult reads what a payment form says about its invoice.
func newInvoiceResult(form tg.PaymentsPaymentFormClass) (invoiceResult, error) {
	var (
		res     invoiceResult
		invoice tg.Invoice
	)
	switch f := form.(type) {
	case *tg.PaymentsPaymentFormStars:
		res = invoiceResult{Kind: invoiceKindStars, FormID: f.FormID, Bot: botRef(f.BotID, f.Users), Title: f.Title, Description: f.Description}
		invoice = f.Invoice
	case *tg.PaymentsPaymentForm:
		res = invoiceResult{Kind: invoiceKindPayment, FormID: f.FormID, Bot: botRef(f.BotID, f.Users), Title: f.Title, Description: f.Description}
		invoice = f.Invoice
	case *tg.PaymentsPaymentFormStarGift:
		res = invoiceResult{Kind: invoiceKindStarGift, FormID: f.FormID}
		invoice = f.Invoice
	default:
		return invoiceResult{}, errors.Errorf("unexpected payment form %T", form)
	}
	res.Currency = invoice.Currency
	res.Test = invoice.Test
	res.TermsURL = invoice.TermsURL
	res.SubscriptionPeriod = invoice.SubscriptionPeriod
	res.Prices = make([]invoicePrice, 0, len(invoice.Prices))
	for _, p := range invoice.Prices {
		res.Prices = append(res.Prices, invoicePrice{Label: p.Label, Amount: p.Amount})
		res.Total += p.Amount
	}
	return res, nil
}

// getInvoiceForm asks Telegram for an invoice's payment form. Asking pays nothing.
func getInvoiceForm(ctx context.Context, api *tg.Client, invoice tg.InputInvoiceClass) (invoiceResult, error) {
	form, err := api.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		return invoiceResult{}, errors.Wrap(err, "payments.getPaymentForm")
	}
	return newInvoiceResult(form)
}

func (a *app) newInvoiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "invoice (<link> | <peer> <message-id>)",
		Short:   "Read an invoice's payment form without paying",
		GroupID: groupMessaging,
		Long: `Ask Telegram for the payment form of an invoice, as a client does before it
shows the payment sheet, and report what the sheet would charge: the title, the
bot, the currency and each price. Nothing is paid; tg has no command that sends
a payment form.

The invoice is an invoice link (what a bot's createInvoiceLink returns:
https://t.me/$<slug>, t.me/invoice/<slug> or tg://invoice?slug=<slug>), or an
invoice message: the chat and the message id.

Amounts are in the currency's smallest unit: whole Stars for XTR, cents for USD.`,
		Example: `  tg invoice 'https://t.me/$AbCdEf123'
  tg invoice @mybot 1234 -o json | jq '.data.total'`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				slug  string
				msgID int
			)
			if len(args) == 1 {
				s, ok := parseInvoiceSlug(args[0])
				if !ok {
					return usageError{errors.Errorf("%q is not an invoice link; for an invoice message give the chat and the message id", args[0])}
				}
				slug = s
			} else {
				id, err := parseMessageID(args[1])
				if err != nil {
					return err
				}
				msgID = id
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				if slug != "" {
					res, err := getInvoiceForm(ctx, api, &tg.InputInvoiceSlug{Slug: slug})
					if err != nil {
						return err
					}
					res.Slug = slug
					return a.printer.Emit(res)
				}
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				p, err := m.Resolve(ctx, args[0])
				if err != nil {
					return err
				}
				res, err := getInvoiceForm(ctx, api, &tg.InputInvoiceMessage{Peer: p.InputPeer(), MsgID: msgID})
				if err != nil {
					return err
				}
				ref := describeManagedPeer(p)
				res.Peer, res.MessageID = &ref, msgID
				return a.printer.Emit(res)
			})
		},
	}
	return cmd
}
