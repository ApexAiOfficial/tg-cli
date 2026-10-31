package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

// chatInfo is the result of `tg chat get`.
type chatInfo struct {
	Peer     peerRef `json:"peer"`
	Verified bool    `json:"verified,omitempty"`
	Scam     bool    `json:"scam,omitempty"`
	Fake     bool    `json:"fake,omitempty"`
}

// MarshalText renders the chat info.
func (c chatInfo) MarshalText(w io.Writer) error {
	var flags string
	for name, on := range map[string]bool{"verified": c.Verified, "scam": c.Scam, "fake": c.Fake} {
		if on {
			flags += " " + name
		}
	}
	_, err := fmt.Fprintf(w, "%s id=%d%s\n", c.Peer.label(), c.Peer.ID, flags)
	return err
}

// chatFullResult is the result of `tg chat full`. Settings fields are filled
// for groups and channels only.
type chatFullResult struct {
	Peer              peerRef `json:"peer"`
	About             string  `json:"about,omitempty"`
	ParticipantsCount int     `json:"participants_count,omitempty"`
	chatSettings
}

// chatSettings is the administrative state of a group or channel.
type chatSettings struct {
	Broadcast           bool     `json:"broadcast,omitempty"`
	Megagroup           bool     `json:"megagroup,omitempty"`
	Forum               bool     `json:"forum,omitempty"`
	LinkedChatID        int64    `json:"linked_chat_id,omitempty"`
	SlowmodeSeconds     int      `json:"slowmode_seconds,omitempty"`
	PinnedMsgID         int      `json:"pinned_msg_id,omitempty"`
	HasPhoto            bool     `json:"has_photo,omitempty"`
	Signatures          bool     `json:"signatures,omitempty"`
	JoinToSend          bool     `json:"join_to_send,omitempty"`
	JoinRequest         bool     `json:"join_request,omitempty"`
	PrehistoryHidden    bool     `json:"prehistory_hidden,omitempty"`
	Antispam            bool     `json:"antispam,omitempty"`
	Noforwards          bool     `json:"noforwards,omitempty"`
	ParticipantsHidden  bool     `json:"participants_hidden,omitempty"`
	DefaultBannedRights []string `json:"default_banned_rights,omitempty"`
	AvailableReactions  string   `json:"available_reactions,omitempty"`
	Reactions           []string `json:"reactions,omitempty"`
	AdminsCount         int      `json:"admins_count,omitempty"`
	OnlineCount         int      `json:"online_count,omitempty"`
}

// fields lists the set settings as key/value pairs, in a stable order, for
// text output.
func (s chatSettings) fields() [][2]string {
	var out [][2]string
	add := func(k string, on bool, v string) {
		if on {
			out = append(out, [2]string{k, v})
		}
	}
	for _, b := range []struct {
		k  string
		on bool
	}{
		{"broadcast", s.Broadcast}, {"megagroup", s.Megagroup}, {"forum", s.Forum}, {"has_photo", s.HasPhoto},
		{settingSignatures, s.Signatures}, {"join_to_send", s.JoinToSend}, {"join_request", s.JoinRequest},
		{"prehistory_hidden", s.PrehistoryHidden}, {settingAntispam, s.Antispam}, {settingNoforwards, s.Noforwards},
		{"participants_hidden", s.ParticipantsHidden},
	} {
		add(b.k, b.on, "yes")
	}
	add("linked_chat_id", s.LinkedChatID != 0, strconv.FormatInt(s.LinkedChatID, 10))
	add("slowmode_seconds", s.SlowmodeSeconds != 0, strconv.Itoa(s.SlowmodeSeconds))
	add("pinned_msg_id", s.PinnedMsgID != 0, strconv.Itoa(s.PinnedMsgID))
	add("admins_count", s.AdminsCount != 0, strconv.Itoa(s.AdminsCount))
	add("online_count", s.OnlineCount != 0, strconv.Itoa(s.OnlineCount))
	add("default_banned_rights", len(s.DefaultBannedRights) > 0, strings.Join(s.DefaultBannedRights, ","))
	reactions := s.AvailableReactions
	if len(s.Reactions) > 0 {
		reactions += " " + strings.Join(s.Reactions, " ")
	}
	add("available_reactions", s.AvailableReactions != "", reactions)
	return out
}

// channelSettings reads the settings of a channel or supergroup.
func channelSettings(ch *tg.Channel, full *tg.ChannelFull) chatSettings {
	s := chatSettings{
		Broadcast:          ch.Broadcast,
		Megagroup:          ch.Megagroup,
		Forum:              ch.Forum,
		Signatures:         ch.Signatures,
		JoinToSend:         ch.JoinToSend,
		JoinRequest:        ch.JoinRequest,
		Noforwards:         ch.Noforwards,
		LinkedChatID:       full.LinkedChatID,
		SlowmodeSeconds:    full.SlowmodeSeconds,
		PinnedMsgID:        full.PinnedMsgID,
		PrehistoryHidden:   full.HiddenPrehistory,
		Antispam:           full.Antispam,
		ParticipantsHidden: full.ParticipantsHidden,
		AdminsCount:        full.AdminsCount,
		OnlineCount:        full.OnlineCount,
	}
	_, s.HasPhoto = full.ChatPhoto.(*tg.Photo)
	if rights, ok := ch.GetDefaultBannedRights(); ok {
		s.DefaultBannedRights = deniedRights(rights)
	}
	s.AvailableReactions, s.Reactions = describeReactions(full.AvailableReactions)
	return s
}

// Reaction modes reported by describeReactions.
const (
	reactionsAll  = "all"
	reactionsSome = "some"
	reactionsNone = "none"
)

// describeReactions renders a chat's reaction setting as reactionsAll,
// reactionsSome (with the allowed emoji) or reactionsNone.
func describeReactions(r tg.ChatReactionsClass) (mode string, emoji []string) {
	switch v := r.(type) {
	case *tg.ChatReactionsAll:
		return reactionsAll, nil
	case *tg.ChatReactionsSome:
		for _, rc := range v.Reactions {
			switch e := rc.(type) {
			case *tg.ReactionEmoji:
				emoji = append(emoji, e.Emoticon)
			case *tg.ReactionCustomEmoji:
				emoji = append(emoji, "custom:"+strconv.FormatInt(e.DocumentID, 10))
			}
		}
		return reactionsSome, emoji
	case *tg.ChatReactionsNone:
		return reactionsNone, nil
	}
	return "", nil
}

// MarshalText renders the full chat info.
func (c chatFullResult) MarshalText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s id=%d\n", c.Peer.label(), c.Peer.ID); err != nil {
		return err
	}
	if c.ParticipantsCount > 0 {
		if _, err := fmt.Fprintf(w, "participants: %d\n", c.ParticipantsCount); err != nil {
			return err
		}
	}
	if c.About != "" {
		if _, err := fmt.Fprintf(w, "about: %s\n", c.About); err != nil {
			return err
		}
	}
	for _, f := range c.fields() {
		if _, err := fmt.Fprintf(w, "%s: %s\n", f[0], f[1]); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "chat",
		Short:   "Inspect a chat",
		GroupID: groupChats,
		Long:    "Get basic or full information about a chat.",
	}
	cmd.AddCommand(a.newChatGetCmd(), a.newChatFullCmd())
	return cmd
}

func (a *app) newChatGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "get <peer>",
		Short:             "Get basic chat info",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				p, err := m.Resolve(ctx, args[0])
				if err != nil {
					return errors.Wrapf(err, "resolve %q", args[0])
				}
				return a.printer.Emit(chatInfo{
					Peer:     describeManagedPeer(p),
					Verified: p.Verified(),
					Scam:     p.Scam(),
					Fake:     p.Fake(),
				})
			})
		},
	}
	return cmd
}

func (a *app) newChatFullCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "full <peer>",
		Short:             "Get full chat info (about, members, settings)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				p, err := m.Resolve(ctx, args[0])
				if err != nil {
					return errors.Wrapf(err, "resolve %q", args[0])
				}

				res := chatFullResult{Peer: describeManagedPeer(p)}
				if err := fillChatFull(ctx, p, &res); err != nil {
					return err
				}
				return a.printer.Emit(res)
			})
		},
	}
	return cmd
}

// fillChatFull populates about/participant count by peer type.
func fillChatFull(ctx context.Context, p peers.Peer, res *chatFullResult) error {
	switch v := p.(type) {
	case peers.User:
		full, err := v.FullRaw(ctx)
		if err != nil {
			return errors.Wrap(err, "get full user")
		}
		res.About = full.About
	case peers.Channel:
		full, err := v.FullRaw(ctx)
		if err != nil {
			return errors.Wrap(err, "get full channel")
		}
		res.About = full.About
		res.ParticipantsCount = full.ParticipantsCount
		res.chatSettings = channelSettings(v.Raw(), full)
	case peers.Chat:
		full, err := v.FullRaw(ctx)
		if err != nil {
			return errors.Wrap(err, "get full chat")
		}
		res.About = full.About
		res.PinnedMsgID = full.PinnedMsgID
		if rights, ok := v.Raw().GetDefaultBannedRights(); ok {
			res.DefaultBannedRights = deniedRights(rights)
		}
		res.AvailableReactions, res.Reactions = describeReactions(full.AvailableReactions)
	}
	return nil
}
