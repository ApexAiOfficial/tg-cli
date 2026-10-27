package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// generalTopicID is the id of a forum's "General" topic, which always exists,
// cannot be deleted and is the default destination for messages that name no
// topic.
const generalTopicID = 1

// topicsPageSize is the page size of messages.getForumTopics (its maximum).
const topicsPageSize = 100

// randomID returns a random int64 suitable for MTProto random_id fields.
func randomID() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, errors.Wrap(err, "read random")
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil //nolint:gosec // random_id, not security-sensitive
}

// topicItem is one forum topic. IconEmojiID is a string because custom emoji
// document ids exceed the integers JSON consumers can represent exactly.
type topicItem struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	IconColor   int    `json:"icon_color,omitempty"`
	IconEmojiID string `json:"icon_emoji_id,omitempty"`
	Closed      bool   `json:"closed,omitempty"`
	Hidden      bool   `json:"hidden,omitempty"`
	Pinned      bool   `json:"pinned,omitempty"`
	TopMessage  int    `json:"top_message,omitempty"`
	UnreadCount int    `json:"unread_count,omitempty"`
	Date        int    `json:"date,omitempty"`
}

// newTopicItem converts a forum topic to its JSON shape.
func newTopicItem(t *tg.ForumTopic) topicItem {
	item := topicItem{
		ID:          t.ID,
		Title:       t.Title,
		IconColor:   t.IconColor,
		Closed:      t.Closed,
		Hidden:      t.Hidden,
		Pinned:      t.Pinned,
		TopMessage:  t.TopMessage,
		UnreadCount: t.UnreadCount,
		Date:        t.Date,
	}
	if t.IconEmojiID != 0 {
		item.IconEmojiID = strconv.FormatInt(t.IconEmojiID, 10)
	}
	return item
}

// topicFlags lists the set boolean states of a topic for text output.
func (t topicItem) topicFlags() string {
	var flags []string
	for _, f := range []struct {
		name string
		on   bool
	}{{"pinned", t.Pinned}, {"closed", t.Closed}, {"hidden", t.Hidden}} {
		if f.on {
			flags = append(flags, f.name)
		}
	}
	if len(flags) == 0 {
		return ""
	}
	return " [" + strings.Join(flags, ",") + "]"
}

// topicsResult is the result of `tg topics list`.
type topicsResult struct {
	Topics []topicItem `json:"topics"`
}

// MarshalText renders one topic per line.
func (r topicsResult) MarshalText(w io.Writer) error {
	for _, t := range r.Topics {
		if _, err := fmt.Fprintf(w, "#%d %s%s\n", t.ID, t.Title, t.topicFlags()); err != nil {
			return err
		}
	}
	return nil
}

// topicCreatedResult is the result of `tg topics create`. It keeps the "ok"
// field the command emitted before it reported the new topic.
type topicCreatedResult struct {
	OK    bool   `json:"ok"`
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// MarshalText renders the new topic's id.
func (r topicCreatedResult) MarshalText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "created topic #%d %s\n", r.ID, r.Title)
	return err
}

// topicIcon is one default forum-topic icon.
type topicIcon struct {
	Emoji      string `json:"emoji"`
	DocumentID string `json:"document_id"`
}

// topicIconsResult is the result of `tg topics icons`.
type topicIconsResult struct {
	Icons []topicIcon `json:"icons"`
}

// MarshalText renders one icon per line.
func (r topicIconsResult) MarshalText(w io.Writer) error {
	for _, i := range r.Icons {
		if _, err := fmt.Fprintf(w, "%s %s\n", i.Emoji, i.DocumentID); err != nil {
			return err
		}
	}
	return nil
}

// topicColors are the only icon colors Telegram accepts for a new topic.
func topicColors() []int {
	return []int{0x6FB9F0, 0xFFD67E, 0xCB86DB, 0x8EEE98, 0xFF93B2, 0xFB6F5F}
}

// parseTopicColor parses a topic icon color given as hex (6FB9F0, 0x6FB9F0,
// #6FB9F0) or decimal, and checks it is one Telegram accepts.
func parseTopicColor(s string) (int, error) {
	v := strings.TrimSpace(s)
	base := 10
	switch {
	case strings.HasPrefix(v, "#"):
		v, base = v[1:], 16
	case strings.HasPrefix(strings.ToLower(v), "0x"):
		v, base = v[2:], 16
	case strings.ContainsAny(strings.ToLower(v), "abcdef") || len(v) == 6:
		base = 16
	}
	n, err := strconv.ParseInt(v, base, 32)
	if err == nil {
		for _, c := range topicColors() {
			if int(n) == c {
				return c, nil
			}
		}
	}
	var allowed []string
	for _, c := range topicColors() {
		allowed = append(allowed, fmt.Sprintf("%06X", c))
	}
	return 0, usageError{errors.Errorf("invalid topic color %q: must be one of %s", s, strings.Join(allowed, ", "))}
}

// parseEmojiID parses a custom emoji document id ("0" clears an icon).
func parseEmojiID(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, usageError{errors.Errorf("invalid icon emoji id %q: use a document id from `tg topics icons`", s)}
	}
	return n, nil
}

// parseTopicIDs parses topic id arguments.
func parseTopicIDs(args []string) ([]int, error) {
	ids := make([]int, 0, len(args))
	for _, arg := range args {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return nil, usageError{errors.Errorf("invalid topic id %q", arg)}
		}
		ids = append(ids, n)
	}
	return ids, nil
}

// createdTopicID finds the id of the topic a messages.createForumTopic call
// created: the id of the service message carrying MessageActionTopicCreate.
func createdTopicID(u tg.UpdatesClass) (int, bool) {
	var updates []tg.UpdateClass
	switch v := u.(type) {
	case *tg.Updates:
		updates = v.Updates
	case *tg.UpdatesCombined:
		updates = v.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{v.Update}
	}
	for _, upd := range updates {
		var msg tg.MessageClass
		switch v := upd.(type) {
		case *tg.UpdateNewChannelMessage:
			msg = v.Message
		case *tg.UpdateNewMessage:
			msg = v.Message
		}
		svc, ok := msg.(*tg.MessageService)
		if !ok {
			continue
		}
		if _, ok := svc.Action.(*tg.MessageActionTopicCreate); ok {
			return svc.ID, true
		}
	}
	return 0, false
}

// topicIconsFromSet extracts emoji and document ids from the default topic
// icon sticker set.
func topicIconsFromSet(set tg.MessagesStickerSetClass) []topicIcon {
	s, ok := set.(*tg.MessagesStickerSet)
	if !ok {
		return nil
	}
	var icons []topicIcon
	for _, dc := range s.Documents {
		doc, ok := dc.(*tg.Document)
		if !ok {
			continue
		}
		icon := topicIcon{DocumentID: strconv.FormatInt(doc.ID, 10)}
		for _, attr := range doc.Attributes {
			if ce, ok := attr.(*tg.DocumentAttributeCustomEmoji); ok {
				icon.Emoji = ce.Alt
			}
		}
		icons = append(icons, icon)
	}
	return icons
}

// listTopics returns every forum topic of peer, following pagination.
func listTopics(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) ([]topicItem, error) {
	var (
		out  []topicItem
		req  = &tg.MessagesGetForumTopicsRequest{Peer: peer, Limit: topicsPageSize}
		seen = map[int]bool{}
	)
	for {
		res, err := api.MessagesGetForumTopics(ctx, req)
		if err != nil {
			return nil, errors.Wrap(err, "messages.getForumTopics")
		}
		var last *tg.ForumTopic
		for _, tc := range res.Topics {
			t, ok := tc.(*tg.ForumTopic)
			if !ok || seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			out = append(out, newTopicItem(t))
			last = t
		}
		if last == nil || len(res.Topics) < topicsPageSize || len(out) >= res.Count {
			return out, nil
		}
		req.OffsetTopic = last.ID
		req.OffsetID = last.TopMessage
		req.OffsetDate = topicOffsetDate(res, last)
	}
}

// topicOffsetDate is the date of a topic's top message, the offset_date the
// next page of messages.getForumTopics continues from.
func topicOffsetDate(res *tg.MessagesForumTopics, t *tg.ForumTopic) int {
	for _, mc := range res.Messages {
		if m, ok := mc.(*tg.Message); ok && m.ID == t.TopMessage {
			return m.Date
		}
		if m, ok := mc.(*tg.MessageService); ok && m.ID == t.TopMessage {
			return m.Date
		}
	}
	return t.Date
}

func (a *app) newTopicsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "topics",
		Short:   "Manage forum topics",
		GroupID: groupChats,
		Long: `List, create, edit, pin, reorder and delete forum topics in supergroups, and
turn forum mode on or off. Post into a topic with ` + "`tg send --topic <id>`" + `.

Topic ids come from ` + "`tg topics list`" + `; the General topic is always #1.`,
	}
	cmd.AddCommand(
		a.newTopicsListCmd(),
		a.newTopicsCreateCmd(),
		a.newTopicsEditCmd(),
		a.newTopicsDeleteCmd(),
		a.newTopicsPinCmd("pin", true),
		a.newTopicsPinCmd("unpin", false),
		a.newTopicsReorderCmd(),
		a.newTopicsIconsCmd(),
		a.newTopicsEnableCmd(),
		a.newTopicsDisableCmd(),
	)
	return cmd
}

// withPeer runs fn with the resolved input peer of arg.
func (a *app) withPeer(cmd *cobra.Command, arg string, fn func(context.Context, *tg.Client, tg.InputPeerClass) error) error {
	return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
		m, err := a.manager(api)
		if err != nil {
			return err
		}
		peer, err := resolvePeer(ctx, m, arg)
		if err != nil {
			return err
		}
		return fn(ctx, api, peer)
	})
}

func (a *app) newTopicsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <peer>",
		Short: "List forum topics",
		Long: `List every forum topic with its id, title and state (pinned, closed, hidden).
JSON output adds icon_color, icon_emoji_id, top_message, unread_count and date.`,
		Example:           "  tg topics list @mygroup\n  tg topics list @mygroup -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withPeer(cmd, args[0], func(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) error {
				topics, err := listTopics(ctx, api, peer)
				if err != nil {
					return err
				}
				return a.printer.Emit(topicsResult{Topics: topics})
			})
		},
	}
}

func (a *app) newTopicsCreateCmd() *cobra.Command {
	var color, emojiID string
	cmd := &cobra.Command{
		Use:   "create <peer> <title>",
		Short: "Create a forum topic",
		Long: `Create a forum topic and print its id. --icon-color picks one of Telegram's six
topic colors (6FB9F0, FFD67E, CB86DB, 8EEE98, FF93B2, FB6F5F); --icon-emoji-id
sets a custom emoji icon from ` + "`tg topics icons`" + ` instead.`,
		Example: `  tg topics create @mygroup "Questions"
  tg topics create @mygroup "Ideas" --icon-emoji-id 5312536423851630001 -o json`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &tg.MessagesCreateForumTopicRequest{Title: args[1]}
			if color != "" {
				c, err := parseTopicColor(color)
				if err != nil {
					return err
				}
				req.SetIconColor(c)
			}
			if emojiID != "" {
				id, err := parseEmojiID(emojiID)
				if err != nil {
					return err
				}
				req.SetIconEmojiID(id)
			}
			rndID, err := randomID()
			if err != nil {
				return err
			}
			req.RandomID = rndID
			return a.withPeer(cmd, args[0], func(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) error {
				req.Peer = peer
				u, err := api.MessagesCreateForumTopic(ctx, req)
				if err != nil {
					return errors.Wrap(err, "messages.createForumTopic")
				}
				id, ok := createdTopicID(u)
				if !ok {
					return errors.New("messages.createForumTopic: no topic id in the response")
				}
				return a.printer.Emit(topicCreatedResult{OK: true, ID: id, Title: args[1]})
			})
		},
	}
	cmd.Flags().StringVar(&color, "icon-color", "", "icon color: 6FB9F0, FFD67E, CB86DB, 8EEE98, FF93B2 or FB6F5F")
	cmd.Flags().StringVar(&emojiID, "icon-emoji-id", "", "custom emoji icon (document id from tg topics icons)")
	return cmd
}

// topicEdit holds the `tg topics edit` flags.
type topicEdit struct {
	title   string
	emojiID string
	closed  bool
	hidden  bool
}

// request builds the edit request from the flags the user set; changed
// reports whether a flag was given on the command line.
func (e topicEdit) request(topicID int, changed func(string) bool) (*tg.MessagesEditForumTopicRequest, error) {
	req := &tg.MessagesEditForumTopicRequest{TopicID: topicID}
	set := false
	if changed("title") {
		req.SetTitle(e.title)
		set = true
	}
	if changed("icon-emoji-id") {
		id, err := parseEmojiID(e.emojiID)
		if err != nil {
			return nil, err
		}
		req.SetIconEmojiID(id)
		set = true
	}
	if changed("closed") {
		req.SetClosed(e.closed)
		set = true
	}
	if changed("hidden") {
		if topicID != generalTopicID {
			return nil, usageError{errors.New("--hidden applies only to the General topic (#1)")}
		}
		req.SetHidden(e.hidden)
		set = true
	}
	if !set {
		return nil, usageError{errors.New("nothing to change: pass --title, --icon-emoji-id, --closed or --hidden")}
	}
	return req, nil
}

func (a *app) newTopicsEditCmd() *cobra.Command {
	var e topicEdit
	cmd := &cobra.Command{
		Use:   "edit <peer> <topic-id>",
		Short: "Rename, re-icon, close or hide a forum topic",
		Long: `Edit a forum topic. Only the flags you pass are changed.

--closed=true stops members posting in the topic (admins still can);
--hidden=true collapses the General topic (#1) and works on no other topic;
--icon-emoji-id 0 removes a custom emoji icon.`,
		Example: `  tg topics edit @mygroup 12 --title "Help"
  tg topics edit @mygroup 12 --closed
  tg topics edit @mygroup 1 --hidden=false`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTopicIDs(args[1:])
			if err != nil {
				return err
			}
			req, err := e.request(ids[0], func(name string) bool { return cmd.Flags().Changed(name) })
			if err != nil {
				return err
			}
			return a.withPeer(cmd, args[0], func(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) error {
				req.Peer = peer
				if _, err := api.MessagesEditForumTopic(ctx, req); err != nil && !tgerr.Is(err, "TOPIC_NOT_MODIFIED") {
					return errors.Wrap(err, "messages.editForumTopic")
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&e.title, "title", "", "new title")
	fs.StringVar(&e.emojiID, "icon-emoji-id", "", "custom emoji icon document id (0 removes it)")
	fs.BoolVar(&e.closed, "closed", false, "close (true) or reopen (false) the topic")
	fs.BoolVar(&e.hidden, "hidden", false, "hide (true) or show (false) the General topic")
	return cmd
}

// deleteTopic deletes a topic and all of its messages, following the
// affected-history offset until Telegram reports nothing left.
func deleteTopic(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, topicID int) error {
	for {
		res, err := api.MessagesDeleteTopicHistory(ctx, &tg.MessagesDeleteTopicHistoryRequest{Peer: peer, TopMsgID: topicID})
		if err != nil {
			return errors.Wrapf(err, "messages.deleteTopicHistory #%d", topicID)
		}
		if res.Offset <= 0 {
			return nil
		}
	}
}

func (a *app) newTopicsDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <peer> <topic-id>...",
		Short: "Delete forum topics and all their messages",
		Long: `Delete one or more forum topics together with every message in them.
Destructive and irreversible: requires --yes. The General topic (#1) cannot be
deleted; hide it with ` + "`tg topics edit <peer> 1 --hidden`" + ` instead.`,
		Example:           "  tg topics delete @mygroup 54 56 --yes",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTopicIDs(args[1:])
			if err != nil {
				return err
			}
			for _, id := range ids {
				if id == generalTopicID {
					return usageError{errors.New("the General topic (#1) cannot be deleted")}
				}
			}
			if !yes {
				return usageError{errors.New("refusing to delete topics without --yes")}
			}
			return a.withPeer(cmd, args[0], func(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) error {
				for _, id := range ids {
					if err := deleteTopic(ctx, api, peer, id); err != nil {
						return err
					}
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm deletion")
	return cmd
}

func (a *app) newTopicsPinCmd(use string, pinned bool) *cobra.Command {
	short := "Pin a forum topic to the top of the topic list"
	if !pinned {
		short = "Unpin a forum topic"
	}
	return &cobra.Command{
		Use:               use + " <peer> <topic-id>",
		Short:             short,
		Example:           fmt.Sprintf("  tg topics %s @mygroup 12", use),
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTopicIDs(args[1:])
			if err != nil {
				return err
			}
			return a.withPeer(cmd, args[0], func(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) error {
				if _, err := api.MessagesUpdatePinnedForumTopic(ctx, &tg.MessagesUpdatePinnedForumTopicRequest{
					Peer:    peer,
					TopicID: ids[0],
					Pinned:  pinned,
				}); err != nil {
					return errors.Wrap(err, "messages.updatePinnedForumTopic")
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
}

func (a *app) newTopicsReorderCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "reorder <peer> <topic-id>...",
		Short: "Set the order of pinned forum topics",
		Long: `Set the order of the pinned topics, first to last. Without --force every listed
topic must already be pinned; with --force the listed topics become exactly the
pinned set, in this order.`,
		Example:           "  tg topics reorder @mygroup 12 14 16 --force",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTopicIDs(args[1:])
			if err != nil {
				return err
			}
			return a.withPeer(cmd, args[0], func(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) error {
				req := &tg.MessagesReorderPinnedForumTopicsRequest{Peer: peer, Order: ids}
				req.SetForce(force)
				if _, err := api.MessagesReorderPinnedForumTopics(ctx, req); err != nil {
					return errors.Wrap(err, "messages.reorderPinnedForumTopics")
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "make the listed topics exactly the pinned set")
	return cmd
}

func (a *app) newTopicsIconsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "icons",
		Short: "List the emoji available as forum topic icons",
		Long: `List Telegram's default forum-topic icons with the document id to pass to
--icon-emoji-id. Any custom emoji works for Premium accounts; these work for all.`,
		Example: "  tg topics icons\n  tg topics icons -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				set, err := api.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
					Stickerset: &tg.InputStickerSetEmojiDefaultTopicIcons{},
				})
				if err != nil {
					return errors.Wrap(err, "messages.getStickerSet")
				}
				return a.printer.Emit(topicIconsResult{Icons: topicIconsFromSet(set)})
			})
		},
	}
}

// toggleForum turns forum mode of a supergroup on or off.
func (a *app) toggleForum(cmd *cobra.Command, arg string, enabled bool) error {
	return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
		m, err := a.manager(api)
		if err != nil {
			return err
		}
		ch, err := asInputChannel(ctx, m, arg)
		if err != nil {
			return err
		}
		if _, err := api.ChannelsToggleForum(ctx, &tg.ChannelsToggleForumRequest{
			Channel: ch,
			Enabled: enabled,
		}); err != nil && !tgIsNotModified(err) {
			return errors.Wrap(err, "channels.toggleForum")
		}
		return a.printer.Emit(okResult{OK: true})
	})
}

func (a *app) newTopicsEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "enable <peer>",
		Short:             "Enable forum topics in a supergroup",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.toggleForum(cmd, args[0], true)
		},
	}
}

func (a *app) newTopicsDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <peer>",
		Short: "Turn forum topics off in a supergroup",
		Long: `Turn forum mode off: the group becomes one flat chat. Existing topics and
their messages are kept and come back if forum mode is enabled again.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.toggleForum(cmd, args[0], false)
		},
	}
}
