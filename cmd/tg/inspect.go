package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Kinds reported by `tg inspect` and the discovery commands.
const (
	kindBroadcast  = "broadcast"  // a channel: only admins post
	kindSupergroup = "supergroup" // a group where members post
	kindGigagroup  = "gigagroup"  // a broadcast group: members read only
	kindForum      = "forum"      // a supergroup organized in topics
	kindGroup      = "group"      // a basic (legacy) group
	chatKindUser   = "user"
	chatKindBot    = "bot"
)

// inspectResult describes one group, channel or user: what it is, how big it
// is, and who may post what. Every field after Input is omitted when unknown.
type inspectResult struct {
	Input string   `json:"input"`
	Peer  *peerRef `json:"peer,omitempty"`
	Kind  string   `json:"kind,omitempty"`
	// Preview is true when only an invite link's preview was readable (the
	// account is not a member and the link allows no peek).
	Preview bool `json:"preview,omitempty"`
	// Limited is true when the chat's full information was refused (a
	// private channel the account cannot read): only its basic object is
	// reported, so about, online, slow mode, bots and the pin are unknown.
	Limited bool `json:"limited,omitempty"`

	Verified   bool     `json:"verified,omitempty"`
	Scam       bool     `json:"scam,omitempty"`
	Fake       bool     `json:"fake,omitempty"`
	Restricted []string `json:"restricted,omitempty"` // platform restriction reasons

	About   string `json:"about,omitempty"`
	Members int    `json:"members,omitempty"`
	Online  int    `json:"online,omitempty"`
	Admins  int    `json:"admins,omitempty"`

	// Member is whether this account belongs to the chat. A channel's date is
	// the join date for members and the creation date otherwise; a basic
	// group's is always its creation date.
	Member    bool `json:"member"`
	Admin     bool `json:"admin,omitempty"`
	JoinedAt  int  `json:"joined_at,omitempty"`
	CreatedAt int  `json:"created_at,omitempty"`

	// JoinRequest: joining needs an admin's approval. JoinToSend: in a
	// channel's discussion group, members must join before they can write.
	JoinRequest bool `json:"join_request,omitempty"`
	JoinToSend  bool `json:"join_to_send,omitempty"`

	// SlowmodeSeconds is the minimum gap between one member's messages.
	SlowmodeSeconds int `json:"slowmode_seconds,omitempty"`
	// PaidMessageStars is what each message costs a member who is not exempt.
	PaidMessageStars int64 `json:"paid_message_stars,omitempty"`
	// BoostsUnrestrict is how many boosts let a member skip slow mode and
	// the default restrictions.
	BoostsUnrestrict int `json:"boosts_unrestrict,omitempty"`
	// SubscriptionStars and SubscriptionDays describe a paid invite link.
	SubscriptionStars int64 `json:"subscription_stars,omitempty"`
	SubscriptionDays  int   `json:"subscription_days,omitempty"`

	// MembersCannot lists what ordinary members may not do (the chat's
	// default banned rights), e.g. "send_messages" or "embed_links".
	MembersCannot []string `json:"members_cannot,omitempty"`
	// ICannot lists this account's own restrictions, if an admin set any.
	ICannot      []string `json:"i_cannot,omitempty"`
	ICannotUntil int      `json:"i_cannot_until,omitempty"`

	HiddenHistory      bool `json:"hidden_history,omitempty"` // new members do not see earlier messages
	Noforwards         bool `json:"noforwards,omitempty"`
	Antispam           bool `json:"antispam,omitempty"` // Telegram's aggressive anti-spam
	ParticipantsHidden bool `json:"participants_hidden,omitempty"`

	LinkedChat *peerRef    `json:"linked_chat,omitempty"` // a channel's discussion group, or a group's channel
	Pinned     *pinnedInfo `json:"pinned,omitempty"`
	Bots       []peerRef   `json:"bots,omitempty"` // bots in the chat (often its moderators)
	Error      *errorInfo  `json:"error,omitempty"`
	Skipped    bool        `json:"skipped,omitempty"` // not inspected: an earlier peer hit a flood wait
}

// pinnedInfo is a chat's pinned message (the most recent pin).
type pinnedInfo struct {
	ID   int    `json:"id"`
	Date int    `json:"date,omitempty"`
	Text string `json:"text,omitempty"`
}

// inspectListResult is the result of `tg inspect`.
type inspectListResult struct {
	Results []inspectResult `json:"results"`
}

// MarshalText renders each result as a short block.
func (r inspectListResult) MarshalText(w io.Writer) error {
	for i, res := range r.Results {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := res.writeText(w); err != nil {
			return err
		}
	}
	return nil
}

func (r inspectResult) writeText(w io.Writer) error {
	var b strings.Builder
	switch {
	case r.Skipped:
		fmt.Fprintf(&b, "%s: skipped (an earlier peer hit a flood wait)\n", r.Input)
	case r.Error != nil:
		fmt.Fprintf(&b, "%s: error: %s\n", r.Input, r.Error.Message)
	default:
		r.writeHeader(&b)
		r.writeRules(&b)
		r.writeContext(&b)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeHeader writes the first line: label, kind, id and flags, then sizes.
func (r inspectResult) writeHeader(b *strings.Builder) {
	label := r.Input
	if r.Peer != nil {
		label = r.Peer.label()
	}
	fmt.Fprintf(b, "%s [%s]", label, r.Kind)
	if r.Peer != nil && r.Peer.ID != 0 {
		fmt.Fprintf(b, " id=%d", r.Peer.ID)
	}
	flags := []struct {
		name string
		on   bool
	}{
		{"preview", r.Preview}, {"limited", r.Limited}, {"member", r.Member}, {"admin", r.Admin},
		{"verified", r.Verified}, {"scam", r.Scam}, {"fake", r.Fake},
	}
	for _, f := range flags {
		if f.on {
			b.WriteString(" " + f.name)
		}
	}
	b.WriteString("\n")
	if r.Members > 0 {
		fmt.Fprintf(b, "  members: %d", r.Members)
		if r.Online > 0 {
			fmt.Fprintf(b, " (online %d)", r.Online)
		}
		b.WriteString("\n")
	}
}

// writeRules writes who may post what.
func (r inspectResult) writeRules(b *strings.Builder) {
	if r.SlowmodeSeconds > 0 {
		fmt.Fprintf(b, "  slow mode: %s\n", time.Duration(r.SlowmodeSeconds)*time.Second)
	}
	if r.PaidMessageStars > 0 {
		fmt.Fprintf(b, "  each message costs %d Stars\n", r.PaidMessageStars)
	}
	if r.SubscriptionStars > 0 {
		fmt.Fprintf(b, "  paid subscription: %d Stars per %d days\n", r.SubscriptionStars, r.SubscriptionDays)
	}
	if r.JoinRequest {
		b.WriteString("  joining needs an admin's approval\n")
	}
	if len(r.MembersCannot) > 0 {
		fmt.Fprintf(b, "  members cannot: %s\n", strings.Join(r.MembersCannot, ", "))
	}
	if len(r.ICannot) > 0 {
		fmt.Fprintf(b, "  this account cannot: %s\n", strings.Join(r.ICannot, ", "))
	}
}

// writeContext writes the bots, the linked chat, the description and the pin.
func (r inspectResult) writeContext(b *strings.Builder) {
	if len(r.Bots) > 0 {
		names := make([]string, 0, len(r.Bots))
		for _, bot := range r.Bots {
			names = append(names, bot.label())
		}
		fmt.Fprintf(b, "  bots: %s\n", strings.Join(names, ", "))
	}
	if r.LinkedChat != nil {
		fmt.Fprintf(b, "  linked: %s\n", r.LinkedChat.label())
	}
	if r.About != "" {
		fmt.Fprintf(b, "  about: %s\n", oneLine(r.About))
	}
	if r.Pinned != nil && r.Pinned.Text != "" {
		fmt.Fprintf(b, "  pinned #%d: %s\n", r.Pinned.ID, oneLine(r.Pinned.Text))
	}
}

// oneLine collapses whitespace so multi-line text fits one line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// channelKind names what a channel is.
func channelKind(ch *tg.Channel) string {
	switch {
	case ch.Gigagroup:
		return kindGigagroup
	case ch.Forum:
		return kindForum
	case ch.Megagroup:
		return kindSupergroup
	default:
		return kindBroadcast
	}
}

// rightEmbedLinks names the right to send links, the one promotion most
// often runs into.
const rightEmbedLinks = "embed_links"

// deniedRights lists the actions a set of banned rights forbids, in a fixed
// order. A banned right that is set means the action is NOT allowed.
func deniedRights(r tg.ChatBannedRights) []string {
	rights := []struct {
		name string
		on   bool
	}{
		{"view_messages", r.ViewMessages},
		{"send_messages", r.SendMessages},
		{"send_plain", r.SendPlain},
		{"send_media", r.SendMedia},
		{"send_photos", r.SendPhotos},
		{"send_videos", r.SendVideos},
		{"send_docs", r.SendDocs},
		{"send_stickers", r.SendStickers},
		{"send_gifs", r.SendGifs},
		{"send_inline", r.SendInline},
		{"send_polls", r.SendPolls},
		{rightEmbedLinks, r.EmbedLinks},
		{"invite_users", r.InviteUsers},
		{"pin_messages", r.PinMessages},
		{"change_info", r.ChangeInfo},
		{"manage_topics", r.ManageTopics},
	}
	var out []string
	for _, right := range rights {
		if right.on {
			out = append(out, right.name)
		}
	}
	return out
}

// restrictionReasons renders platform restrictions as "platform:reason".
func restrictionReasons(rr []tg.RestrictionReason) []string {
	out := make([]string, 0, len(rr))
	for _, r := range rr {
		out = append(out, r.Platform+":"+r.Reason)
	}
	return out
}

// buildChannelInspect fills a result from a channel and its full info. users
// and chats are the entities that came with the full info.
func buildChannelInspect(res *inspectResult, ch *tg.Channel, full *tg.ChannelFull, users []tg.UserClass, chats []tg.ChatClass) {
	ref := peerRef{ID: ch.ID, Type: peerChannel, Name: ch.Title, Username: ch.Username}
	res.Peer = &ref
	res.Kind = channelKind(ch)
	res.Verified, res.Scam, res.Fake = ch.Verified, ch.Scam, ch.Fake
	res.Restricted = restrictionReasons(ch.RestrictionReason)
	res.Member = !ch.Left
	if res.Member {
		res.JoinedAt = ch.Date
	} else {
		res.CreatedAt = ch.Date
	}
	res.JoinRequest, res.JoinToSend = ch.JoinRequest, ch.JoinToSend
	res.PaidMessageStars = ch.SendPaidMessagesStars
	if count, ok := ch.GetParticipantsCount(); ok {
		res.Members = count
	}
	if rights, ok := ch.GetDefaultBannedRights(); ok {
		res.MembersCannot = deniedRights(rights)
	}
	if rights, ok := ch.GetBannedRights(); ok {
		res.ICannot = deniedRights(rights)
		res.ICannotUntil = rights.UntilDate
	}
	if _, ok := ch.GetAdminRights(); ok || ch.Creator {
		res.Admin = true
	}
	res.Noforwards = ch.Noforwards
	if full == nil {
		return
	}

	res.About = full.About
	if full.ParticipantsCount > 0 {
		res.Members = full.ParticipantsCount
	}
	res.Online, res.Admins = full.OnlineCount, full.AdminsCount
	res.SlowmodeSeconds = full.SlowmodeSeconds
	if full.SendPaidMessagesStars > 0 {
		res.PaidMessageStars = full.SendPaidMessagesStars
	}
	res.BoostsUnrestrict = full.BoostsUnrestrict
	res.HiddenHistory = full.HiddenPrehistory
	res.Antispam = full.Antispam
	res.ParticipantsHidden = full.ParticipantsHidden
	if full.PinnedMsgID != 0 {
		res.Pinned = &pinnedInfo{ID: full.PinnedMsgID}
	}

	ent := entitiesOf(users, chats)
	if linked, ok := full.GetLinkedChatID(); ok && linked != 0 {
		ref := describePeer(&tg.PeerChannel{ChannelID: linked}, ent)
		res.LinkedChat = &ref
	}
	for _, bi := range full.BotInfo {
		id, ok := bi.GetUserID()
		if !ok {
			continue
		}
		res.Bots = append(res.Bots, describePeer(&tg.PeerUser{UserID: id}, ent))
	}
}

// buildChatInspect fills a result from a basic group and its full info.
func buildChatInspect(res *inspectResult, c *tg.Chat, full *tg.ChatFull, users []tg.UserClass) {
	ref := peerRef{ID: c.ID, Type: peerChat, Name: c.Title}
	res.Peer = &ref
	res.Kind = kindGroup
	res.Members = c.ParticipantsCount
	res.Member = !c.Left && !c.Deactivated
	res.CreatedAt = c.Date // a basic group's date is its creation date
	if rights, ok := c.GetDefaultBannedRights(); ok {
		res.MembersCannot = deniedRights(rights)
	}
	if _, ok := c.GetAdminRights(); ok || c.Creator {
		res.Admin = true
	}
	if full == nil {
		return
	}
	res.About = full.About
	if full.PinnedMsgID != 0 {
		res.Pinned = &pinnedInfo{ID: full.PinnedMsgID}
	}
	ent := entitiesOf(users, nil)
	for _, bi := range full.BotInfo {
		if id, ok := bi.GetUserID(); ok {
			res.Bots = append(res.Bots, describePeer(&tg.PeerUser{UserID: id}, ent))
		}
	}
}

// buildInviteInspect fills a result from an invite link's preview.
func buildInviteInspect(res *inspectResult, inv *tg.ChatInvite) {
	res.Peer = &peerRef{Type: peerUnknown, Name: inv.Title}
	switch {
	case inv.Broadcast:
		res.Kind, res.Peer.Type = kindBroadcast, peerChannel
	case inv.Megagroup:
		res.Kind, res.Peer.Type = kindSupergroup, peerChannel
	case inv.Channel:
		res.Peer.Type = peerChannel
	default:
		res.Kind, res.Peer.Type = kindGroup, peerChat
	}
	res.Preview = true
	res.About = inv.About
	res.Members = inv.ParticipantsCount
	res.JoinRequest = inv.RequestNeeded
	res.Verified, res.Scam, res.Fake = inv.Verified, inv.Scam, inv.Fake
	if pricing, ok := inv.GetSubscriptionPricing(); ok {
		res.SubscriptionStars = pricing.Amount
		res.SubscriptionDays = pricing.Period / 86400
	}
}

// inviteHashOf reports whether arg is an invite link (t.me/+hash,
// t.me/joinchat/hash or +hash) and returns its hash. A "+" followed only by
// digits is a phone number, not an invite.
func inviteHashOf(arg string) (string, bool) {
	s := strings.TrimSpace(arg)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimPrefix(s, "www.")
	for _, host := range []string{"t.me/", "telegram.me/", "telegram.dog/"} {
		if strings.HasPrefix(s, host) {
			rest := strings.TrimPrefix(s, host)
			switch {
			case strings.HasPrefix(rest, "+"):
				return trimInvite(strings.TrimPrefix(rest, "+"))
			case strings.HasPrefix(rest, "joinchat/"):
				return trimInvite(strings.TrimPrefix(rest, "joinchat/"))
			}
			return "", false
		}
	}
	if strings.HasPrefix(s, "+") {
		hash := strings.TrimPrefix(s, "+")
		if strings.Trim(hash, "0123456789") == "" {
			return "", false // a phone number
		}
		return trimInvite(hash)
	}
	return "", false
}

func trimInvite(hash string) (string, bool) {
	if i := strings.IndexAny(hash, "/?#"); i >= 0 {
		hash = hash[:i]
	}
	return hash, hash != ""
}

func (a *app) newInspectCmd() *cobra.Command {
	var (
		interval time.Duration
		noPinned bool
	)

	cmd := &cobra.Command{
		Use:   "inspect <peer|invite-link>...",
		Short: "Inspect groups and channels before taking part",
		Long: `Report what a group, channel or user is, without joining or posting:
its kind (broadcast channel, supergroup, forum, gigagroup, basic group),
member and online counts, description, slow mode, what ordinary members may
not do (default banned rights, e.g. embed_links), whether each message costs
Stars, whether joining needs approval, the linked channel or discussion group,
the bots in it (often its moderators) and the pinned message (often its rules).

Invite links (t.me/+hash, t.me/joinchat/hash) are previewed with
messages.checkChatInvite, which does not join. A public chat is read without
joining too.

Several peers are inspected one after another, --interval apart, to stay
within Telegram's limits on resolving usernames. Per-peer failures are
reported in the result; a flood wait stops the run and marks the remaining
peers as skipped. The exit code is that of the first failure.`,
		Example: `  tg inspect @somegroup
  tg inspect https://t.me/+AbCdEf123 @another --interval 5s -o json`,
		GroupID: groupChats,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				out, firstErr, err := inspectAll(ctx, api, m, args, interval, !noPinned)
				if err != nil {
					return err
				}
				if err := a.printer.Emit(out); err != nil {
					return err
				}
				if firstErr != nil {
					return reportedError{exit: classifyError(firstErr).Exit}
				}
				return nil
			})
		},
	}

	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "pause between peers")
	cmd.Flags().BoolVar(&noPinned, "no-pinned", false, "do not fetch the pinned message's text")

	return cmd
}

// inspectAll inspects each argument in turn, interval apart. A per-peer failure
// is recorded in its result and the first one is returned as firstErr; after a
// flood wait the remaining peers are marked skipped. err is set only when the
// context ends.
func inspectAll(
	ctx context.Context, api *tg.Client, m *peerManager, args []string, interval time.Duration, withPinned bool,
) (out inspectListResult, firstErr, err error) {
	out.Results = make([]inspectResult, 0, len(args))
	flooded := false
	for i, arg := range args {
		if flooded {
			out.Results = append(out.Results, inspectResult{Input: arg, Skipped: true})
			continue
		}
		if i > 0 && interval > 0 {
			if serr := sleepCtx(ctx, interval); serr != nil {
				return out, firstErr, serr
			}
		}
		res, ierr := inspectOne(ctx, api, m, arg, withPinned)
		if ierr != nil {
			// Keep whatever was learned before the failure.
			info := classifyError(ierr)
			res.Input, res.Error = arg, &info
			if firstErr == nil {
				firstErr = ierr
			}
			_, flooded = tgerr.AsFloodWait(ierr)
		}
		out.Results = append(out.Results, res)
	}
	return out, firstErr, nil
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// inspectOne inspects one peer or invite link.
func inspectOne(ctx context.Context, api *tg.Client, m *peerManager, arg string, withPinned bool) (inspectResult, error) {
	res := inspectResult{Input: arg}
	if hash, ok := inviteHashOf(arg); ok {
		invite, err := api.MessagesCheckChatInvite(ctx, hash)
		if err != nil {
			return res, errors.Wrap(err, "messages.checkChatInvite")
		}
		switch v := invite.(type) {
		case *tg.ChatInvite:
			buildInviteInspect(&res, v)
			return res, nil
		case *tg.ChatInviteAlready:
			return inspectChatClass(ctx, api, res, v.Chat, withPinned)
		case *tg.ChatInvitePeek:
			return inspectChatClass(ctx, api, res, v.Chat, withPinned)
		default:
			return res, errors.Errorf("unexpected invite type %T", invite)
		}
	}

	p, err := resolveManagedPeer(ctx, m, arg)
	if err != nil {
		return res, errors.Wrapf(err, "resolve %q", arg)
	}
	switch v := p.(type) {
	case peers.Channel:
		return inspectChatClass(ctx, api, res, v.Raw(), withPinned)
	case peers.Chat:
		return inspectChatClass(ctx, api, res, v.Raw(), withPinned)
	case peers.User:
		ref := describeManagedPeer(p)
		res.Peer = &ref
		res.Kind = chatKindUser
		if v.Raw().Bot {
			res.Kind = chatKindBot
		}
		res.Verified, res.Scam, res.Fake = p.Verified(), p.Scam(), p.Fake()
		full, err := v.FullRaw(ctx)
		if err != nil {
			return res, errors.Wrap(err, "users.getFullUser")
		}
		res.About = full.About
		return res, nil
	default:
		return res, errors.Errorf("unsupported peer type %T", p)
	}
}

// inspectChatClass inspects a channel or basic group from its raw object.
func inspectChatClass(ctx context.Context, api *tg.Client, res inspectResult, chat tg.ChatClass, withPinned bool) (inspectResult, error) {
	switch c := chat.(type) {
	case *tg.Channel:
		full, err := api.ChannelsGetFullChannel(ctx, c.AsInput())
		if err != nil {
			// A private channel the account cannot read still has its
			// basic object: report that much.
			if tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_PUBLIC_GROUP_NA") {
				buildChannelInspect(&res, c, nil, nil, nil)
				res.Limited = true
				return res, nil
			}
			return res, errors.Wrap(err, "channels.getFullChannel")
		}
		cf, ok := full.FullChat.(*tg.ChannelFull)
		if !ok {
			return res, errors.Errorf("unexpected full chat type %T", full.FullChat)
		}
		buildChannelInspect(&res, c, cf, full.Users, full.Chats)
		if withPinned && res.Pinned != nil {
			fillPinned(ctx, res.Pinned, func() (tg.MessagesMessagesClass, error) {
				return api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
					Channel: c.AsInput(),
					ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: res.Pinned.ID}},
				})
			})
		}
		return res, nil
	case *tg.ChannelForbidden:
		res.Peer = &peerRef{ID: c.ID, Type: peerChannel, Name: c.Title}
		res.Kind = kindBroadcast
		if c.Megagroup {
			res.Kind = kindSupergroup
		}
		return res, notFoundf("banned from this channel, or it is private")
	case *tg.Chat:
		full, err := api.MessagesGetFullChat(ctx, c.ID)
		if err != nil {
			return res, errors.Wrap(err, "messages.getFullChat")
		}
		cf, _ := full.FullChat.(*tg.ChatFull)
		buildChatInspect(&res, c, cf, full.Users)
		if withPinned && res.Pinned != nil {
			fillPinned(ctx, res.Pinned, func() (tg.MessagesMessagesClass, error) {
				return api.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: res.Pinned.ID}})
			})
		}
		return res, nil
	default:
		return res, errors.Errorf("unsupported chat type %T", chat)
	}
}

// fillPinned adds the pinned message's date and text. A failure leaves only
// the id: the pin is a nice-to-have, not a reason to fail the inspection.
func fillPinned(_ context.Context, pin *pinnedInfo, fetch func() (tg.MessagesMessagesClass, error)) {
	res, err := fetch()
	if err != nil {
		return
	}
	msgs, _, err := messagesFrom(res)
	if err != nil {
		return
	}
	for _, mc := range msgs {
		if msg, ok := mc.(*tg.Message); ok && msg.ID == pin.ID {
			pin.Date = msg.Date
			pin.Text = msg.Message
			return
		}
	}
}
