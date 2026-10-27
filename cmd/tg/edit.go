package main

import (
	"context"
	"fmt"
	"io"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

func (a *app) newEditCmd() *cobra.Command {
	var (
		useHTML   bool
		fromStdin bool
	)

	cmd := &cobra.Command{
		Use:     "edit <peer> <message-id> [text]",
		Short:   "Edit a message",
		GroupID: groupMessaging,
		Long: `Edit the text of a message you sent. The peer is me/self, @username, phone,
or a t.me link. The new text may come from --stdin.`,
		Example: `  tg edit @durov 12345 "updated text"
  tg edit me 1000 --html "<b>bold</b>"`,
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseMessageID(args[1])
			if err != nil {
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
				bf, err := builderFor(ctx, m, sender, args[0])
				if err != nil {
					return err
				}

				opt := styling.Plain(text)
				if useHTML {
					opt = html.String(nil, text)
				}
				edited, err := editMessage(ctx, bf, id, opt)
				if err != nil {
					return err
				}
				return a.printer.Emit(editedResult{Peer: args[0], MessageID: edited})
			})
		},
	}

	cmd.Flags().BoolVar(&useHTML, "html", false, "use HTML styling")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the new text from standard input")

	return cmd
}

// editedResult is the result of `tg edit`. Its JSON matches sentResult.
type editedResult struct {
	Peer      string `json:"peer,omitempty"`
	MessageID int    `json:"message_id"`
}

// MarshalText renders a short acknowledgement.
func (r editedResult) MarshalText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "edited message #%d\n", r.MessageID)
	return err
}

// editMessage edits message id and returns it. Telegram answers with edit
// updates (for a channel post with a linked discussion group, the group copy's
// first), which unpack.MessageID does not read, so the RPC's error is the only
// result. MESSAGE_NOT_MODIFIED means the message already has the text.
func editMessage(ctx context.Context, b *message.RequestBuilder, id int, opt styling.StyledTextOption) (int, error) {
	if _, err := b.Edit(id).StyledText(ctx, opt); err != nil && !tg.IsMessageNotModified(err) {
		return 0, errors.Wrap(err, "edit")
	}
	return id, nil
}
