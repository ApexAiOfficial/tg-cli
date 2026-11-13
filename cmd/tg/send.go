package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// previewOptions choose the link preview a sent message shows. Without them
// Telegram previews the first link in the text, if any.
type previewOptions struct {
	url   string // the page to preview, which need not appear in the text
	large bool   // a large image instead of a thumbnail
	above bool   // the preview above the text instead of below it
}

func (o *previewOptions) register(fs *pflag.FlagSet) {
	fs.StringVar(&o.url, "preview-url", "",
		"show the link preview of this http(s) URL, whether or not the text links to it")
	fs.BoolVar(&o.large, "large-preview", false, "with --preview-url: a large preview image")
	fs.BoolVar(&o.above, "preview-above", false, "with --preview-url: show the preview above the text")
}

// validate checks the preview flags against each other and --nowebpage.
func (o previewOptions) validate(noWebpage bool) error {
	if o.url == "" {
		if o.large || o.above {
			return usageError{errors.New("--large-preview and --preview-above need --preview-url")}
		}
		return nil
	}
	if noWebpage {
		return usageError{errors.New("--preview-url and --nowebpage contradict each other")}
	}
	u, err := url.Parse(o.url)
	if err != nil || !isWebScheme(u.Scheme) || u.Host == "" {
		return usageError{errors.Errorf("--preview-url must be an http(s) URL, got %q", o.url)}
	}
	return nil
}

// sendStyled sends the styled text through b, as plain text or, with a preview
// URL, as a message whose media is that page's preview. The preview is
// optional: if Telegram cannot build it, the message goes out without one.
func sendStyled(
	ctx context.Context, api *tg.Client, b *message.Builder, options []styling.StyledTextOption, preview previewOptions,
) (int, error) {
	if preview.url == "" {
		return sentMessageID(b.StyledText(ctx, options...))
	}
	warmPreview(ctx, api, preview.url, time.Second)
	if preview.above {
		b = b.InvertMedia()
	}
	wp := message.WebPage(preview.url, options...).ForceLargeMedia(preview.large).Optional(true)
	return sentMessageID(b.Media(ctx, wp))
}

// previewWarmAttempts bounds how often warmPreview asks for a pending preview.
const previewWarmAttempts = 6

// warmPreview asks Telegram to build the link preview for pageURL, waiting while it
// is pending: a page Telegram has not fetched yet cannot be attached
// (WEBPAGE_NOT_FOUND). Failures are ignored; the send then decides.
func warmPreview(ctx context.Context, api *tg.Client, pageURL string, pause time.Duration) {
	for attempt := 1; ; attempt++ {
		res, err := api.MessagesGetWebPagePreview(ctx, &tg.MessagesGetWebPagePreviewRequest{Message: pageURL})
		if err != nil {
			return
		}
		media, ok := res.Media.(*tg.MessageMediaWebPage)
		if !ok {
			return
		}
		if _, pending := media.Webpage.(*tg.WebPagePending); !pending {
			return
		}
		if attempt == previewWarmAttempts || sleepCtx(ctx, pause) != nil {
			return // still pending: the send goes out without waiting longer
		}
	}
}

// sentResult is the result of a command that sends a message. With --wait it
// also carries the peer's response.
type sentResult struct {
	Peer      string `json:"peer,omitempty"`
	MessageID int    `json:"message_id"`
	*conversation
}

// MarshalText renders a short acknowledgement and any collected response.
func (r sentResult) MarshalText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "sent message #%d\n", r.MessageID); err != nil {
		return err
	}
	return r.marshalText(w)
}

func (a *app) newSendCmd() *cobra.Command {
	var (
		peer      string
		fromStdin bool
		msg       messageOptions
		preview   previewOptions
		wait      waitOptions
	)

	cmd := &cobra.Command{
		Use:     "send [flags] [text]",
		Short:   "Send a message to a peer",
		GroupID: groupMessaging,
		Long: `Send a text message. With no --peer, the message goes to your own
Saved Messages, which is handy for notes and agent self-messaging.

A peer with no username or phone can be addressed by its numeric id with the
"id:" prefix (the "id" field in ` + "`tg chats list --output json`" + `), provided
it has been cached first by running ` + "`tg chats list`" + `.

With --wait, tg listens for the peer's response before sending and returns the
replies (and edits) it collected: it stops when the peer has been quiet for
--settle after responding, when --replies messages arrived, or at --wait.`,
		Example: `  # Message yourself (Saved Messages)
  tg send "hello"

  # Message a channel or user
  tg send --peer @durov "hi there"

  # Message a peer without a username, by numeric id (cache it first)
  tg chats list >/dev/null && tg send --peer id:2201861038 "hi"

  # Post into a forum topic (ids from tg topics list)
  tg send --peer @mygroup --topic 12 "Welcome to this topic"

  # HTML formatting, sent silently
  tg send --peer @durov --html --silent "<b>bold</b>"

  # A hidden link in the text, and a large preview of another page above it
  tg send --peer @mygroup --html --preview-url https://example.com --large-preview --preview-above \
    '<b>New!</b> Try <a href="https://t.me/mybot?start=src1">@mybot</a>'

  # Test a bot: send a command and collect its replies until it goes quiet
  tg send --peer @mybot /start --wait 30s -o json

  # Keep a secret out of the process list
  printf '%s' "$CODE" | tg send --peer @mybot --stdin --wait 20s`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := msg.validateTopic(); err != nil {
				return err
			}
			text, err := messageText(args, fromStdin, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if err := preview.validate(msg.noWebpage); err != nil {
				return err
			}

			collector := newReplyCollector()
			return a.run(cmd.Context(), wait.runParams(collector), func(ctx context.Context, api *tg.Client) error {
				sender, m, err := a.sender(api)
				if err != nil {
					return err
				}
				bf, err := targetFor(ctx, m, sender, peer, collector, wait)
				if err != nil {
					return err
				}

				b, options := msg.apply(bf, text)
				id, err := sendStyled(ctx, api, b, options, preview)
				if err != nil {
					return errors.Wrap(err, "send")
				}
				res := sentResult{Peer: peer, MessageID: id}
				collector.setAction(id, id)
				if res.conversation, err = awaitResponse(ctx, collector, wait); err != nil {
					return err
				}
				return a.printer.Emit(res)
			})
		},
	}

	fs := cmd.Flags()
	fs.StringVarP(&peer, "peer", "p", "",
		"peer to write (username, phone, deep link, or id:<n>); default: yourself")
	fs.BoolVar(&fromStdin, "stdin", false, "read the message text from standard input")
	msg.register(fs)
	msg.registerTopic(fs)
	preview.register(fs)
	wait.register(fs)
	registerPeerCompletion(cmd, "peer")

	return cmd
}
