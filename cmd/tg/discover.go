package main

import (
	"context"
	"fmt"
	"io"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

func (a *app) newResolveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resolve <username>",
		Short:   "Resolve a username to a peer",
		GroupID: groupChats,
		Long:    "Resolve a @username (or t.me link) to its peer id, type and name.",
		Example: "  tg resolve @durov\n  tg resolve durov --output json",
		Args:    cobra.ExactArgs(1),
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
				return a.printer.Emit(describeManagedPeer(p))
			})
		},
	}
	return cmd
}

// discoveredPeer is a peer found by a discovery command: a peerRef plus what
// kind of chat it is and how many members it has, when Telegram says.
type discoveredPeer struct {
	peerRef
	Kind    string `json:"kind,omitempty"`
	Members int    `json:"members,omitempty"`
}

// discoveredListResult is the result of `tg search-public` and `tg similar`.
type discoveredListResult struct {
	Peers []discoveredPeer `json:"peers"`
	// Total is how many results Telegram has when it returned only some.
	Total int `json:"total,omitempty"`
}

// MarshalText renders one peer per line: type, label, id, kind and members.
func (r discoveredListResult) MarshalText(w io.Writer) error {
	for _, p := range r.Peers {
		line := fmt.Sprintf("%s\t%s\tid=%d", p.Type, p.label(), p.ID)
		if p.Kind != "" {
			line += "\t" + p.Kind
		}
		if p.Members > 0 {
			line += fmt.Sprintf("\tmembers=%d", p.Members)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	if r.Total > len(r.Peers) {
		if _, err := fmt.Fprintf(w, "(%d of %d shown)\n", len(r.Peers), r.Total); err != nil {
			return err
		}
	}
	return nil
}

// describeDiscovered describes a found peer with its kind and member count.
func describeDiscovered(pc tg.PeerClass, ent peer.Entities) discoveredPeer {
	d := discoveredPeer{peerRef: describePeer(pc, ent)}
	switch v := pc.(type) {
	case *tg.PeerChannel:
		if ch, ok := ent.Channel(v.ChannelID); ok {
			d.Kind = channelKind(ch)
			d.Members, _ = ch.GetParticipantsCount()
		}
	case *tg.PeerChat:
		d.Kind = kindGroup
		if c, ok := ent.Chat(v.ChatID); ok {
			d.Members = c.ParticipantsCount
		}
	case *tg.PeerUser:
		d.Kind = chatKindUser
		if u, ok := ent.User(v.UserID); ok && u.Bot {
			d.Kind = chatKindBot
		}
	}
	return d
}

// discoveredFromChats describes the channels and groups in a chat list.
func discoveredFromChats(chats []tg.ChatClass) []discoveredPeer {
	ent := entitiesOf(nil, chats)
	out := make([]discoveredPeer, 0, len(chats))
	for _, c := range chats {
		switch v := c.(type) {
		case *tg.Channel:
			out = append(out, describeDiscovered(&tg.PeerChannel{ChannelID: v.ID}, ent))
		case *tg.Chat:
			out = append(out, describeDiscovered(&tg.PeerChat{ChatID: v.ID}, ent))
		}
	}
	return out
}

func (a *app) newSearchPublicCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:     "search-public <query>",
		Short:   "Search public chats and users",
		GroupID: groupChats,
		Long: `Search Telegram's public directory of users, groups and channels.
Each result names its kind (broadcast, supergroup, forum, gigagroup, group,
user or bot) and, for chats, the member count when Telegram includes it.`,
		Example: "  tg search-public gopher\n  tg search-public news --limit 20",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				found, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: args[0], Limit: limit})
				if err != nil {
					return errors.Wrap(err, "contacts.search")
				}
				ent := entitiesOf(found.Users, found.Chats)

				var out discoveredListResult
				seen := map[int64]bool{}
				for _, group := range [][]tg.PeerClass{found.MyResults, found.Results} {
					for _, pc := range group {
						ref := describeDiscovered(pc, ent)
						if seen[ref.ID] {
							continue
						}
						seen[ref.ID] = true
						out.Peers = append(out.Peers, ref)
					}
				}
				return a.printer.Emit(out)
			})
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "maximum number of results")

	return cmd
}

func (a *app) newSimilarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "similar [channel]",
		Short:   "List channels Telegram recommends as similar",
		GroupID: groupChats,
		Long: `List the channels Telegram recommends as similar to a public channel
(channels.getChannelRecommendations), with their kind and member count. With
no argument it lists the channels Telegram recommends to this account.
Accounts without Premium see only the first few; "total" says how many exist.`,
		Example: "  tg similar @somechannel\n  tg similar -o json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				req := &tg.ChannelsGetChannelRecommendationsRequest{}
				if len(args) == 1 {
					m, err := a.manager(api)
					if err != nil {
						return err
					}
					p, err := resolveManagedPeer(ctx, m, args[0])
					if err != nil {
						return errors.Wrapf(err, "resolve %q", args[0])
					}
					ch, ok := p.(peers.Channel)
					if !ok {
						return usageError{errors.New("not a channel or supergroup")}
					}
					req.SetChannel(ch.InputChannel())
				}
				res, err := api.ChannelsGetChannelRecommendations(ctx, req)
				if err != nil {
					return errors.Wrap(err, "channels.getChannelRecommendations")
				}
				var out discoveredListResult
				switch v := res.(type) {
				case *tg.MessagesChats:
					out.Peers = discoveredFromChats(v.Chats)
				case *tg.MessagesChatsSlice:
					out.Peers = discoveredFromChats(v.Chats)
					out.Total = v.Count
				default:
					return errors.Errorf("unexpected result type %T", res)
				}
				return a.printer.Emit(out)
			})
		},
	}
	return cmd
}

func (a *app) newSubscribeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "subscribe <peer>",
		Aliases: []string{"join"},
		Short:   "Join a public channel or group",
		GroupID: groupChats,
		Long:    "Join (subscribe to) a public channel or supergroup by username or link.",
		Example: "  tg subscribe @durov\n  tg join @somechannel",
		Args:    cobra.ExactArgs(1),
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
				ch, ok := p.(peers.Channel)
				if !ok {
					return errors.New("not a channel or supergroup")
				}
				if _, err := api.ChannelsJoinChannel(ctx, ch.InputChannel()); err != nil {
					return errors.Wrap(err, "channels.joinChannel")
				}
				return a.printer.Emit(okResult{OK: true})
			})
		},
	}
	return cmd
}
