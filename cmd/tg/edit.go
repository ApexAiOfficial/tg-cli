package main

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// editWithPreview edits message id in peer to the styled text, with the link
// preview of preview.url as its media. Telegram's text-only edit drops the
// preview's position and may rebuild the card from the first link in the new
// text, so a post sent with --preview-url is edited with the same flags to
// keep its card. The preview is optional: without one the edit still applies.
func editWithPreview(
	ctx context.Context, api *tg.Client, peer tg.InputPeerClass, id int, opt styling.StyledTextOption, preview previewOptions,
) (int, error) {
	tb := entity.Builder{}
	if err := styling.Perform(&tb, opt); err != nil {
		return 0, err
	}
	msg, entities := tb.Complete()
	warmPreview(ctx, api, preview.url, time.Second)
	return sentMessageID(api.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
		Peer:        peer,
		ID:          id,
		Message:     msg,
		Entities:    entities,
		InvertMedia: preview.above,
		Media: &tg.InputMediaWebPage{
			URL:             preview.url,
			ForceLargeMedia: preview.large,
			Optional:        true,
		},
	}))
}

func (a *app) newEditCmd() *cobra.Command {
	var (
		useHTML   bool
		fromStdin bool
		preview   previewOptions
	)

	cmd := &cobra.Command{
		Use:     "edit <peer> <message-id> [text]",
		Short:   "Edit a message",
		GroupID: groupMessaging,
		Long: `Edit the text of a message you sent. The peer is me/self, @username, phone,
or a t.me link. The new text may come from --stdin.

A message sent with --preview-url keeps its card only if the edit passes the
same --preview-url, --large-preview and --preview-above: a plain text edit lets
Telegram move the preview below the text or rebuild it from the first link.`,
		Example: `  tg edit @durov 12345 "updated text"
  tg edit me 1000 --html "<b>bold</b>"
  tg edit @mychannel 30 --html --stdin --preview-url https://example.com --large-preview --preview-above`,
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseMessageID(args[1])
			if err != nil {
				return err
			}
			if err := preview.validate(false); err != nil {
				return err
			}
			text, err := messageText(args[2:], fromStdin, cmd.InOrStdin())
			if err != nil {
				return err
			}

			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				sender, m, err := a.sender(api)
				if err != nil {
					return err
				}

				opt := styling.Plain(text)
				if useHTML {
					opt = html.String(nil, text)
				}
				var newID int
				if preview.url != "" {
					peer, perr := resolvePeer(ctx, m, args[0])
					if perr != nil {
						return perr
					}
					newID, err = editWithPreview(ctx, api, peer, id, opt, preview)
				} else {
					bf, berr := builderFor(ctx, m, sender, args[0])
					if berr != nil {
						return berr
					}
					newID, err = sentMessageID(bf.Edit(id).StyledText(ctx, opt))
				}
				if err != nil {
					return errors.Wrap(err, "edit")
				}
				return a.printer.Emit(sentResult{Peer: args[0], MessageID: newID})
			})
		},
	}

	cmd.Flags().BoolVar(&useHTML, "html", false, "use HTML styling")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the new text from standard input")
	preview.register(cmd.Flags())

	return cmd
}
