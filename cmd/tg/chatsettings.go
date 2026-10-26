package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// rightField names one default member right and points at its flag in a
// ChatBannedRights. A set flag means members are denied that right.
type rightField struct {
	name string
	flag *bool
}

// rightFields lists the default member rights of r that `tg set-permissions`
// manages, in Telegram's settings order.
func rightFields(r *tg.ChatBannedRights) []rightField {
	return []rightField{
		{"send_messages", &r.SendMessages},
		{"send_media", &r.SendMedia},
		{"send_plain", &r.SendPlain},
		{"send_photos", &r.SendPhotos},
		{"send_videos", &r.SendVideos},
		{"send_roundvideos", &r.SendRoundvideos},
		{"send_audios", &r.SendAudios},
		{"send_voices", &r.SendVoices},
		{"send_docs", &r.SendDocs},
		{"send_stickers", &r.SendStickers},
		{"send_gifs", &r.SendGifs},
		{"send_games", &r.SendGames},
		{"send_inline", &r.SendInline},
		{"send_polls", &r.SendPolls},
		{"send_reactions", &r.SendReactions},
		{"embed_links", &r.EmbedLinks},
		{"change_info", &r.ChangeInfo},
		{"invite_users", &r.InviteUsers},
		{"pin_messages", &r.PinMessages},
		{"manage_topics", &r.ManageTopics},
	}
}

// rightNames lists every right name rightFields knows.
func rightNames() []string {
	var r tg.ChatBannedRights
	fields := rightFields(&r)
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.name)
	}
	return names
}

// deniedRights lists the rights r denies to members.
func deniedRights(r tg.ChatBannedRights) []string {
	var out []string
	for _, f := range rightFields(&r) {
		if *f.flag {
			out = append(out, f.name)
		}
	}
	return out
}

// splitNames splits comma-separated flag values, dropping blanks.
func splitNames(values []string) []string {
	var out []string
	for _, v := range values {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// applyRights returns current with the deny names denied and the allow names
// allowed. Flags it does not manage are kept as they were. A right named in
// both lists is a usage error.
func applyRights(current tg.ChatBannedRights, deny, allow []string) (tg.ChatBannedRights, error) {
	next := current
	fields := map[string]*bool{}
	for _, f := range rightFields(&next) {
		fields[f.name] = f.flag
	}
	want := map[string]bool{}
	for _, list := range []struct {
		names []string
		deny  bool
	}{{deny, true}, {allow, false}} {
		for _, name := range list.names {
			if _, ok := fields[name]; !ok {
				return current, usageError{errors.Errorf("unknown right %q (known: %s)", name, strings.Join(rightNames(), ", "))}
			}
			if prev, seen := want[name]; seen && prev != list.deny {
				return current, usageError{errors.Errorf("right %q is both denied and allowed", name)}
			}
			want[name] = list.deny
		}
	}
	for name, d := range want {
		*fields[name] = d
	}
	return next, nil
}

// permissionsResult is the result of `tg set-permissions`.
type permissionsResult struct {
	Denied []string `json:"denied"`
}

// MarshalText renders the denied rights.
func (r permissionsResult) MarshalText(w io.Writer) error {
	denied := "none"
	if len(r.Denied) > 0 {
		denied = strings.Join(r.Denied, ", ")
	}
	_, err := fmt.Fprintf(w, "denied to members: %s\n", denied)
	return err
}

// currentDefaultRights fetches a group's default member rights from Telegram
// rather than the local cache, so a delta never applies to stale state.
func currentDefaultRights(ctx context.Context, api *tg.Client, p peers.Peer) (tg.ChatBannedRights, error) {
	var (
		chats tg.MessagesChatsClass
		err   error
	)
	switch v := p.(type) {
	case peers.Channel:
		chats, err = api.ChannelsGetChannels(ctx, []tg.InputChannelClass{v.InputChannel()})
	case peers.Chat:
		chats, err = api.MessagesGetChats(ctx, []int64{v.ID()})
	default:
		return tg.ChatBannedRights{}, usageError{errors.New("peer is not a group or channel")}
	}
	if err != nil {
		return tg.ChatBannedRights{}, errors.Wrap(err, "get chat")
	}
	for _, c := range chats.GetChats() {
		switch v := c.(type) {
		case *tg.Channel:
			r, _ := v.GetDefaultBannedRights()
			return r, nil
		case *tg.Chat:
			r, _ := v.GetDefaultBannedRights()
			return r, nil
		}
	}
	return tg.ChatBannedRights{}, notFoundf("chat %d not returned", p.ID())
}

func (a *app) newSetPermissionsCmd() *cobra.Command {
	var deny, allow []string
	cmd := &cobra.Command{
		Use:     "set-permissions <peer>",
		Short:   "Change what members of a group may do",
		GroupID: groupChats,
		Long: `Change a group's default member permissions. --deny takes rights away from all
members, --allow gives them back; rights not named keep their current value.
With neither flag the current denied rights are printed.

Rights: ` + strings.Join(rightNames(), ", ") + `.`,
		Example: `  tg set-permissions @mygroup
  tg set-permissions @mygroup --deny manage_topics,pin_messages,change_info
  tg set-permissions @mygroup --allow send_polls`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			deny, allow := splitNames(deny), splitNames(allow)
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				p, err := resolvePeerArg(ctx, m, args[0])
				if err != nil {
					return err
				}
				current, err := currentDefaultRights(ctx, api, p)
				if err != nil {
					return err
				}
				next, err := applyRights(current, deny, allow)
				if err != nil {
					return err
				}
				if len(deny)+len(allow) > 0 {
					if _, err := api.MessagesEditChatDefaultBannedRights(ctx, &tg.MessagesEditChatDefaultBannedRightsRequest{
						Peer:         p.InputPeer(),
						BannedRights: next,
					}); err != nil && !tgIsNotModified(err) {
						return errors.Wrap(err, "messages.editChatDefaultBannedRights")
					}
				}
				return a.printer.Emit(permissionsResult{Denied: append([]string{}, deniedRights(next)...)})
			})
		},
	}
	cmd.Flags().StringSliceVar(&deny, "deny", nil, "rights to take away from members (comma-separated)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "rights to give back to members (comma-separated)")
	return cmd
}

// tgIsNotModified reports Telegram's CHAT_NOT_MODIFIED, which means the
// requested state is already in place.
func tgIsNotModified(err error) bool {
	return tgerr.Is(err, tg.ErrChatNotModified)
}

// wrapRPC tags a non-nil RPC error with the method name; nil stays nil.
func wrapRPC(err error, method string) error {
	if err != nil {
		return errors.Wrap(err, method)
	}
	return nil
}

// reactionsRequest builds the available-reactions setting from the flags.
func reactionsRequest(all, none bool, emoji []string) (tg.ChatReactionsClass, error) {
	chosen := 0
	for _, on := range []bool{all, none, len(emoji) > 0} {
		if on {
			chosen++
		}
	}
	if chosen != 1 {
		return nil, usageError{errors.New("pass exactly one of --all, --none or --emoji")}
	}
	switch {
	case all:
		return &tg.ChatReactionsAll{}, nil
	case none:
		return &tg.ChatReactionsNone{}, nil
	}
	some := &tg.ChatReactionsSome{}
	for _, e := range emoji {
		some.Reactions = append(some.Reactions, &tg.ReactionEmoji{Emoticon: e})
	}
	return some, nil
}

func (a *app) newSetReactionsCmd() *cobra.Command {
	var (
		all, none bool
		emoji     []string
	)
	cmd := &cobra.Command{
		Use:     "set-reactions <peer>",
		Short:   "Choose which reactions a group or channel allows",
		GroupID: groupChats,
		Long: `Set the reactions members can use: every standard reaction (--all), none
(--none), or only the listed emoji (--emoji).`,
		Example: `  tg set-reactions @mychannel --emoji 👍,🔥,❤,🎉
  tg set-reactions @mygroup --all`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			reactions, err := reactionsRequest(all, none, splitNames(emoji))
			if err != nil {
				return err
			}
			return a.withPeer(cmd, args[0], func(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) error {
				if _, err := api.MessagesSetChatAvailableReactions(ctx, &tg.MessagesSetChatAvailableReactionsRequest{
					Peer:               peer,
					AvailableReactions: reactions,
				}); err != nil && !tgIsNotModified(err) {
					return errors.Wrap(err, "messages.setChatAvailableReactions")
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
	fs := cmd.Flags()
	fs.BoolVar(&all, "all", false, "allow every standard reaction")
	fs.BoolVar(&none, "none", false, "turn reactions off")
	fs.StringSliceVar(&emoji, "emoji", nil, "allow only these emoji (comma-separated)")
	return cmd
}

func (a *app) newLinkDiscussionCmd() *cobra.Command {
	var unlink bool
	cmd := &cobra.Command{
		Use:     "link-discussion <channel> [group]",
		Short:   "Link a discussion group to a channel",
		GroupID: groupChats,
		Long: `Make a supergroup the discussion group of a channel: each new channel post is
forwarded into the group and gets a comment thread there. --unlink removes the
link. Posts published before the link was made get no comments.`,
		Example: `  tg link-discussion @mychannel @mygroup
  tg link-discussion @mychannel --unlink`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			if unlink == (len(args) == 2) {
				return usageError{errors.New("pass a group to link, or --unlink without one")}
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				broadcast, err := asInputChannel(ctx, m, args[0])
				if err != nil {
					return err
				}
				var group tg.InputChannelClass = &tg.InputChannelEmpty{}
				if !unlink {
					if group, err = asInputChannel(ctx, m, args[1]); err != nil {
						return err
					}
				}
				if _, err := api.ChannelsSetDiscussionGroup(ctx, &tg.ChannelsSetDiscussionGroupRequest{
					Broadcast: broadcast,
					Group:     group,
				}); err != nil {
					return errors.Wrap(err, "channels.setDiscussionGroup")
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
	cmd.Flags().BoolVar(&unlink, "unlink", false, "remove the channel's discussion group")
	return cmd
}

// chatToggles maps each `tg chat-toggle` setting to the call that sets it.
func chatToggles() map[string]func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
	channelOnly := func(p peers.Peer) (tg.InputChannelClass, error) {
		ch, ok := p.(peers.Channel)
		if !ok {
			return nil, usageError{errors.New("this setting needs a channel or supergroup")}
		}
		return ch.InputChannel(), nil
	}
	return map[string]func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error{
		"signatures": func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
			ch, err := channelOnly(p)
			if err != nil {
				return err
			}
			_, err = api.ChannelsToggleSignatures(ctx, &tg.ChannelsToggleSignaturesRequest{Channel: ch, SignaturesEnabled: on})
			return wrapRPC(err, "channels.toggleSignatures")
		},
		"join-to-send": func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
			ch, err := channelOnly(p)
			if err != nil {
				return err
			}
			_, err = api.ChannelsToggleJoinToSend(ctx, &tg.ChannelsToggleJoinToSendRequest{Channel: ch, Enabled: on})
			return wrapRPC(err, "channels.toggleJoinToSend")
		},
		"join-request": func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
			ch, err := channelOnly(p)
			if err != nil {
				return err
			}
			_, err = api.ChannelsToggleJoinRequest(ctx, &tg.ChannelsToggleJoinRequestRequest{Channel: ch, Enabled: on})
			return wrapRPC(err, "channels.toggleJoinRequest")
		},
		"prehistory-hidden": func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
			ch, err := channelOnly(p)
			if err != nil {
				return err
			}
			_, err = api.ChannelsTogglePreHistoryHidden(ctx, &tg.ChannelsTogglePreHistoryHiddenRequest{Channel: ch, Enabled: on})
			return wrapRPC(err, "channels.togglePreHistoryHidden")
		},
		"antispam": func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
			ch, err := channelOnly(p)
			if err != nil {
				return err
			}
			_, err = api.ChannelsToggleAntiSpam(ctx, &tg.ChannelsToggleAntiSpamRequest{Channel: ch, Enabled: on})
			return wrapRPC(err, "channels.toggleAntiSpam")
		},
		"participants-hidden": func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
			ch, err := channelOnly(p)
			if err != nil {
				return err
			}
			_, err = api.ChannelsToggleParticipantsHidden(ctx, &tg.ChannelsToggleParticipantsHiddenRequest{Channel: ch, Enabled: on})
			return wrapRPC(err, "channels.toggleParticipantsHidden")
		},
		"noforwards": func(ctx context.Context, api *tg.Client, p peers.Peer, on bool) error {
			_, err := api.MessagesToggleNoForwards(ctx, &tg.MessagesToggleNoForwardsRequest{Peer: p.InputPeer(), Enabled: on})
			return wrapRPC(err, "messages.toggleNoForwards")
		},
	}
}

// chatToggleNames lists the `tg chat-toggle` settings in sorted order.
func chatToggleNames() []string {
	toggles := chatToggles()
	names := make([]string, 0, len(toggles))
	for name := range toggles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseOnOff parses an on/off argument.
func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, usageError{errors.Errorf("invalid value %q: use on or off", s)}
}

func (a *app) newChatToggleCmd() *cobra.Command {
	names := chatToggleNames()
	cmd := &cobra.Command{
		Use:     "chat-toggle <peer> <setting> on|off",
		Short:   "Turn a group or channel setting on or off",
		GroupID: groupChats,
		Long: `Turn one group or channel setting on or off. Settings:

  signatures           sign channel posts with the author's name
  join-to-send         members must join a discussion group before writing in it
  join-request         new members need admin approval
  prehistory-hidden    hide earlier history from new members
  antispam             Telegram's aggressive anti-spam (large groups only)
  participants-hidden  hide the member list
  noforwards           stop forwarding and saving of messages`,
		Example: `  tg chat-toggle @mygroup join-to-send on
  tg chat-toggle @mychannel signatures off`,
		Args: cobra.ExactArgs(3),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 1:
				return names, cobra.ShellCompDirectiveNoFileComp
			case 2:
				return []string{"on", "off"}, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			toggle, ok := chatToggles()[args[1]]
			if !ok {
				return usageError{errors.Errorf("unknown setting %q (known: %s)", args[1], strings.Join(names, ", "))}
			}
			on, err := parseOnOff(args[2])
			if err != nil {
				return err
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				p, err := resolvePeerArg(ctx, m, args[0])
				if err != nil {
					return err
				}
				if err := toggle(ctx, api, p, on); err != nil && !tgIsNotModified(err) {
					return err
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
	return cmd
}
