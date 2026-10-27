package main

import (
	"context"

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
				if err := editMessage(ctx, bf, id, opt); err != nil {
					return err
				}
				return a.printer.Emit(sentResult{Peer: args[0], MessageID: id})
			})
		},
	}

	cmd.Flags().BoolVar(&useHTML, "html", false, "use HTML styling")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the new text from standard input")

	return cmd
}

// editMessage edits message id. Telegram answers with edit updates (for a
// channel post with a linked discussion group, also one for the group's copy),
// which unpack.MessageID does not read, so the RPC's error is the only result.
func editMessage(ctx context.Context, b *message.RequestBuilder, id int, opt styling.StyledTextOption) error {
	if _, err := b.Edit(id).StyledText(ctx, opt); err != nil {
		return errors.Wrap(err, "edit")
	}
	return nil
}
