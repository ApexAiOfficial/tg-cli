package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
)

// activityMsg is the part of a message that activity statistics need.
type activityMsg struct {
	Date      int
	SenderID  int64 // 0 when unknown (e.g. an anonymous admin)
	SenderBot bool
	Text      string
	URLs      []string // links in the text, its entities and URL buttons
	Mentions  []string // @usernames mentioned
	Forward   bool
	Media     bool
	Reply     bool
	Views     int
	// Service is a service message's action, e.g. actionChatAddUser; such
	// messages are counted apart from the others.
	Service string
}

// activityResult summarizes recent activity in a chat.
type activityResult struct {
	Peer peerRef `json:"peer"`
	// Sampled counts the ordinary messages read (service messages apart).
	Sampled int `json:"sampled"`
	Service int `json:"service,omitempty"`
	// Joins counts "joined" service messages in the window (often hidden).
	Joins int `json:"joins,omitempty"`

	From      int     `json:"from,omitempty"` // unix time of the oldest message
	To        int     `json:"to,omitempty"`   // unix time of the newest
	SpanHours float64 `json:"span_hours"`
	PerDay    float64 `json:"per_day"`
	// Truncated is true when --limit cut the sample short, so the window
	// holds more messages than were read; per_day is then the rate over the
	// sampled span. Otherwise it is the rate over WindowDays (or the span).
	Truncated    bool    `json:"truncated,omitempty"`
	WindowDays   int     `json:"window_days,omitempty"`
	ActiveDays   int     `json:"active_days"`
	LastAgeHours float64 `json:"last_age_hours"`

	Senders        int     `json:"senders"`
	TopSenderShare float64 `json:"top5_sender_share"` // share of messages by the 5 busiest senders

	LinkShare      float64 `json:"link_share"`      // messages with a link
	BotLinkShare   float64 `json:"bot_link_share"`  // messages linking to or mentioning a bot
	MentionShare   float64 `json:"mention_share"`   // messages that mention an @username
	ForwardShare   float64 `json:"forward_share"`   // forwarded messages
	MediaShare     float64 `json:"media_share"`     // messages with media
	ReplyShare     float64 `json:"reply_share"`     // replies: a sign of conversation
	BotShare       float64 `json:"bot_share"`       // messages sent by bots
	DuplicateShare float64 `json:"duplicate_share"` // text already seen in the sample (copy-paste spam)
	MedianViews    int     `json:"median_views,omitempty"`

	ByHour    [24]int `json:"by_hour_utc"`
	ByWeekday [7]int  `json:"by_weekday_utc"` // Sunday first
	PeakHours []int   `json:"peak_hours_utc"` // the three busiest hours, busiest first

	TopDomains []countItem   `json:"top_domains,omitempty"`
	Promoters  promoterStats `json:"promoters"`
}

// countItem is a name with a count.
type countItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// promoterStats describes the senders who post links: how many, and how often
// one of them posts a link again. The gap between one sender's link posts is
// what the chat tolerates in practice.
type promoterStats struct {
	Senders        int     `json:"senders"` // senders with at least one link message
	Repeat         int     `json:"repeat"`  // of those, senders with two or more
	MedianGapHours float64 `json:"median_gap_hours,omitempty"`
	MinGapHours    float64 `json:"min_gap_hours,omitempty"`
}

var bareURL = regexp.MustCompile(`(?i)\b(?:https?://|www\.|t\.me/|telegram\.me/)[^\s<>()]+`)

// collectURLs gathers a message's links: URL and text-link entities, bare
// links in the text and URL buttons.
func collectURLs(msg *tg.Message) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), ".,;:!?)]}'\"")
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	if ents, ok := msg.GetEntities(); ok {
		for _, e := range newEntityItems(msg.Message, ents) {
			switch e.Type {
			case "url":
				add(e.Text)
			case "text_url":
				add(e.URL)
			}
		}
	}
	for _, u := range bareURL.FindAllString(msg.Message, -1) {
		add(u)
	}
	if rm, ok := msg.GetReplyMarkup(); ok {
		if inline, ok := rm.(*tg.ReplyInlineMarkup); ok {
			for _, row := range inline.Rows {
				for _, b := range row.Buttons {
					if ub, ok := b.(*tg.KeyboardButtonURL); ok {
						add(ub.URL)
					}
				}
			}
		}
	}
	return out
}

var mentionRe = regexp.MustCompile(`@([A-Za-z][A-Za-z0-9_]{3,31})`)

// newActivityMsg reduces a raw message for the statistics.
func newActivityMsg(mc tg.NotEmptyMessage, ent peer.Entities) (activityMsg, bool) {
	switch m := mc.(type) {
	case *tg.Message:
		am := activityMsg{Date: m.Date, Text: m.Message, URLs: collectURLs(m), Views: m.Views}
		if from, ok := m.GetFromID(); ok {
			switch f := from.(type) {
			case *tg.PeerUser:
				am.SenderID = f.UserID
				if u, ok := ent.User(f.UserID); ok {
					am.SenderBot = u.Bot
				}
			case *tg.PeerChannel:
				am.SenderID = -f.ChannelID
			case *tg.PeerChat:
				am.SenderID = -f.ChatID
			}
		} else if pc, ok := m.PeerID.(*tg.PeerChannel); ok && m.Post {
			am.SenderID = -pc.ChannelID // a channel post
		}
		if _, ok := m.GetViaBotID(); ok {
			am.SenderBot = true
		}
		for _, sub := range mentionRe.FindAllStringSubmatch(m.Message, -1) {
			am.Mentions = append(am.Mentions, sub[1])
		}
		_, am.Forward = m.GetFwdFrom()
		if media, ok := m.GetMedia(); ok && mediaType(media) != "" && mediaType(media) != "webpage" {
			am.Media = true
		}
		am.Reply = isRealReply(m)
		return am, true
	case *tg.MessageService:
		action := snakeCase(strings.TrimPrefix(m.Action.TypeName(), "messageAction"))
		return activityMsg{Date: m.Date, Service: action}, true
	default:
		return activityMsg{}, false
	}
}

// isRealReply reports whether a message answers another message. In a forum
// every message carries a reply header naming its topic; only one that also
// names the topic's top message (reply_to_top_id) answers a message.
func isRealReply(m *tg.Message) bool {
	rt, ok := m.GetReplyTo()
	if !ok {
		return false
	}
	h, ok := rt.(*tg.MessageReplyHeader)
	if !ok || h.ReplyToMsgID == 0 {
		return false
	}
	if h.ForumTopic {
		_, inTopic := h.GetReplyToTopID()
		return inTopic
	}
	return true
}

// isBotName reports whether a username names a bot (Telegram requires bot
// usernames to end in "bot").
func isBotName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "bot")
}

// linksToBot reports whether a message links to or mentions a bot.
func linksToBot(m activityMsg) bool {
	for _, name := range m.Mentions {
		if isBotName(name) {
			return true
		}
	}
	for _, raw := range m.URLs {
		host, path := splitURL(raw)
		if !isTelegramLinkHost(host) && host != "telegram.dog" {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if isBotName(first) {
			return true
		}
	}
	return false
}

// splitURL returns a link's lowercase host (without "www.") and path,
// tolerating links written without a scheme.
func splitURL(raw string) (host, path string) {
	s := raw
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), u.Path
}

// normalizeText reduces a message to a comparable form for duplicate
// detection: lower case, digits and links stripped, whitespace collapsed.
func normalizeText(s string) string {
	s = bareURL.ReplaceAllString(strings.ToLower(s), "")
	s = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// activityWindow describes how a sample was read.
type activityWindow struct {
	Truncated bool // the read stopped at --limit, not at the window's start
	Exhausted bool // the read reached the start of the chat's history
	Days      int  // the requested window (--days), 0 for none
}

// Service actions that mean someone joined the chat.
const (
	actionChatAddUser         = "chat_add_user"
	actionChatJoinedByLink    = "chat_joined_by_link"
	actionChatJoinedByRequest = "chat_joined_by_request"
)

// activityTally accumulates per-message counts for computeActivity.
type activityTally struct {
	senders   map[int64]int
	days      map[string]bool
	domains   map[string]int
	texts     map[string]bool
	linkTimes map[int64][]int
	views     []int

	links, botLinks, mentions, forwards, media, replies, bots, dups int
}

func newActivityTally() *activityTally {
	return &activityTally{
		senders:   map[int64]int{},
		days:      map[string]bool{},
		domains:   map[string]int{},
		texts:     map[string]bool{},
		linkTimes: map[int64][]int{},
	}
}

// countIf adds one to *n when cond holds.
func countIf(n *int, cond bool) {
	if cond {
		*n++
	}
}

// add counts one ordinary message, and its time into res's histograms.
func (t *activityTally) add(m activityMsg, res *activityResult) {
	res.From = min(res.From, m.Date)
	res.To = max(res.To, m.Date)
	at := time.Unix(int64(m.Date), 0).UTC()
	res.ByHour[at.Hour()]++
	res.ByWeekday[int(at.Weekday())]++
	t.days[at.Format("2006-01-02")] = true
	if m.SenderID != 0 {
		t.senders[m.SenderID]++
	}
	if len(m.URLs) > 0 {
		t.links++
		t.addLinks(m)
	}
	countIf(&t.botLinks, linksToBot(m))
	countIf(&t.mentions, len(m.Mentions) > 0)
	countIf(&t.forwards, m.Forward)
	countIf(&t.media, m.Media)
	countIf(&t.replies, m.Reply)
	countIf(&t.bots, m.SenderBot)
	if norm := normalizeText(m.Text); len(norm) >= 20 {
		countIf(&t.dups, t.texts[norm])
		t.texts[norm] = true
	}
	if m.Views > 0 {
		t.views = append(t.views, m.Views)
	}
}

// addLinks records a link message's domains and its sender's link times.
func (t *activityTally) addLinks(m activityMsg) {
	for _, u := range m.URLs {
		if host, _ := splitURL(u); host != "" {
			t.domains[host]++
		}
	}
	if m.SenderID != 0 {
		t.linkTimes[m.SenderID] = append(t.linkTimes[m.SenderID], m.Date)
	}
}

// topShare returns how many messages the k busiest senders wrote.
func (t *activityTally) topShare(k int) int {
	counts := make([]int, 0, len(t.senders))
	for _, c := range t.senders {
		counts = append(counts, c)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(counts)))
	top := 0
	for i := 0; i < len(counts) && i < k; i++ {
		top += counts[i]
	}
	return top
}

// computeActivity summarizes msgs (in any order) as of now.
func computeActivity(ref peerRef, msgs []activityMsg, now time.Time, window activityWindow) activityResult {
	res := activityResult{Peer: ref, Truncated: window.Truncated, WindowDays: window.Days}
	var ordinary []activityMsg
	for _, m := range msgs {
		switch m.Service {
		case "":
			ordinary = append(ordinary, m)
		case actionChatAddUser, actionChatJoinedByLink, actionChatJoinedByRequest:
			res.Service++
			res.Joins++
		default:
			res.Service++
		}
	}
	res.Sampled = len(ordinary)
	if res.Sampled == 0 {
		return res
	}

	t := newActivityTally()
	res.From, res.To = ordinary[0].Date, ordinary[0].Date
	for _, m := range ordinary {
		t.add(m, &res)
	}

	n := float64(res.Sampled)
	share := func(k int) float64 { return round3(float64(k) / n) }
	res.LinkShare, res.BotLinkShare, res.MentionShare = share(t.links), share(t.botLinks), share(t.mentions)
	res.ForwardShare, res.MediaShare, res.ReplyShare = share(t.forwards), share(t.media), share(t.replies)
	res.BotShare, res.DuplicateShare = share(t.bots), share(t.dups)

	res.SpanHours = round1(float64(res.To-res.From) / 3600)
	res.ActiveDays = len(t.days)
	res.LastAgeHours = round1(now.Sub(time.Unix(int64(res.To), 0)).Hours())
	res.PerDay = round1(perDay(n, res.To-res.From, int(now.Unix())-res.From, window))

	res.Senders = len(t.senders)
	res.TopSenderShare = share(t.topShare(5))
	res.PeakHours = peakHours(res.ByHour, 3)
	res.TopDomains = topCounts(t.domains, 10)
	res.MedianViews = medianInt(t.views)
	res.Promoters = promoters(t.linkTimes)
	return res
}

// perDay is the message rate. A truncated sample is a busy chat read only
// back to its oldest sampled message, so the rate is over that span (at
// least an hour). A complete sample covers the requested window, or, when the
// chat's history starts inside it (or no window was asked for), the time
// since its first message (at least a day), so a short burst in a quiet chat
// does not read as a high daily rate. sinceFirst is the time from the oldest
// sampled message to now.
func perDay(n float64, spanSeconds, sinceFirstSeconds int, window activityWindow) float64 {
	const day = 86400.0
	switch {
	case window.Truncated:
		return n / math.Max(float64(spanSeconds)/day, 1.0/24)
	case window.Days > 0 && !window.Exhausted:
		return n / float64(window.Days)
	case window.Days > 0:
		return n / math.Max(math.Min(float64(window.Days), float64(sinceFirstSeconds)/day), 1)
	default:
		return n / math.Max(float64(sinceFirstSeconds)/day, 1)
	}
}

// peakHours returns the k busiest hours, busiest first (ties: earlier hour).
func peakHours(byHour [24]int, k int) []int {
	hours := make([]int, 0, 24)
	for h, c := range byHour {
		if c > 0 {
			hours = append(hours, h)
		}
	}
	sort.SliceStable(hours, func(i, j int) bool { return byHour[hours[i]] > byHour[hours[j]] })
	if len(hours) > k {
		hours = hours[:k]
	}
	return hours
}

// topCounts returns the k largest counts, largest first (ties: by name).
func topCounts(m map[string]int, k int) []countItem {
	out := make([]countItem, 0, len(m))
	for name, c := range m {
		out = append(out, countItem{Name: name, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// promoters summarizes how often link posters post links again.
func promoters(linkTimes map[int64][]int) promoterStats {
	var st promoterStats
	var gaps []float64
	for _, times := range linkTimes {
		st.Senders++
		if len(times) < 2 {
			continue
		}
		st.Repeat++
		sort.Ints(times)
		for i := 1; i < len(times); i++ {
			gaps = append(gaps, float64(times[i]-times[i-1])/3600)
		}
	}
	if len(gaps) > 0 {
		sort.Float64s(gaps)
		st.MinGapHours = round1(gaps[0])
		st.MedianGapHours = round1(medianFloat(gaps))
	}
	return st
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// medianFloat expects a sorted, non-empty slice.
func medianFloat(s []float64) float64 {
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// MarshalText renders the summary for people.
func (r activityResult) MarshalText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d messages", r.Peer.label(), r.Sampled)
	if r.Sampled == 0 {
		b.WriteString("\n")
		_, err := io.WriteString(w, b.String())
		return err
	}
	more := ""
	if r.Truncated {
		more = ", more before"
	}
	fmt.Fprintf(&b, " over %.1f h%s (%.1f/day, %d active days), last %.1f h ago\n", r.SpanHours, more, r.PerDay, r.ActiveDays, r.LastAgeHours)
	fmt.Fprintf(&b, "  senders: %d (top 5 wrote %.0f%%), bots %.0f%%\n", r.Senders, r.TopSenderShare*100, r.BotShare*100)
	fmt.Fprintf(&b, "  links %.0f%% (to bots %.0f%%), @mentions %.0f%%, forwards %.0f%%, media %.0f%%, replies %.0f%%, duplicates %.0f%%\n",
		r.LinkShare*100, r.BotLinkShare*100, r.MentionShare*100, r.ForwardShare*100, r.MediaShare*100, r.ReplyShare*100, r.DuplicateShare*100)
	if r.MedianViews > 0 {
		fmt.Fprintf(&b, "  median views: %d\n", r.MedianViews)
	}
	hours := make([]string, 0, len(r.PeakHours))
	for _, h := range r.PeakHours {
		hours = append(hours, fmt.Sprintf("%02d:00", h))
	}
	fmt.Fprintf(&b, "  peak hours (UTC): %s\n", strings.Join(hours, ", "))
	days := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	parts := make([]string, 0, 7)
	for i, c := range r.ByWeekday {
		parts = append(parts, fmt.Sprintf("%s %d", days[i], c))
	}
	fmt.Fprintf(&b, "  by weekday (UTC): %s\n", strings.Join(parts, ", "))
	if p := r.Promoters; p.Senders > 0 {
		fmt.Fprintf(&b, "  link posters: %d, %d posted links again", p.Senders, p.Repeat)
		if p.Repeat > 0 {
			fmt.Fprintf(&b, " (median gap %.1f h, shortest %.1f h)", p.MedianGapHours, p.MinGapHours)
		}
		b.WriteString("\n")
	}
	if len(r.TopDomains) > 0 {
		names := make([]string, 0, len(r.TopDomains))
		for _, d := range r.TopDomains {
			names = append(names, fmt.Sprintf("%s %d", d.Name, d.Count))
		}
		fmt.Fprintf(&b, "  top domains: %s\n", strings.Join(names, ", "))
	}
	if r.Joins > 0 {
		fmt.Fprintf(&b, "  joins in window: %d\n", r.Joins)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (a *app) newActivityCmd() *cobra.Command {
	var (
		limit int
		days  int
	)

	cmd := &cobra.Command{
		Use:   "activity <peer>",
		Short: "Summarize a chat's recent activity",
		Long: `Read a chat's recent messages and summarize its activity: messages per
day, active days, distinct senders and how concentrated they are, the share
of messages with links, links to bots, forwards, media, replies and repeated
text, a UTC histogram by hour and weekday with the busiest hours, the most
linked domains, and how often the same sender posts links again (what the
chat tolerates in practice). Channels also report median views.

It reads history only: it never joins, posts or marks anything read. A public
group can be read without joining. Use --days to bound the window by age and
--limit by count; it stops at whichever comes first.`,
		Example: `  tg activity @somegroup
  tg activity @somegroup --limit 1000 --days 14 -o json`,
		GroupID:           groupChats,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 {
				return usageError{errors.New("--limit must be at least 1")}
			}
			if days < 0 {
				return usageError{errors.New("--days must not be negative")}
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				p, err := resolveManagedPeer(ctx, m, args[0])
				if err != nil {
					return errors.Wrapf(err, "resolve %q", args[0])
				}
				now := time.Now()
				cutoff := 0
				if days > 0 {
					cutoff = int(now.Add(-time.Duration(days) * 24 * time.Hour).Unix())
				}
				msgs, window, err := readActivity(ctx, api, p.InputPeer(), limit, cutoff)
				if err != nil {
					return err
				}
				window.Days = days
				return a.printer.Emit(computeActivity(describeManagedPeer(p), msgs, now, window))
			})
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 300, "maximum number of messages to read")
	cmd.Flags().IntVar(&days, "days", 0, "only messages from the last N days (0 = no age limit)")

	return cmd
}

// readActivity reads up to limit messages, newest first, stopping at the first
// message older than cutoff (a unix time; 0 for none). The window says why it
// stopped: at the limit, at the start of the history, or (neither) at cutoff.
func readActivity(
	ctx context.Context, api *tg.Client, ip tg.InputPeerClass, limit, cutoff int,
) (msgs []activityMsg, window activityWindow, err error) {
	iter := query.Messages(api).GetHistory(ip).BatchSize(max(1, min(limit, 100))).Iter()
	for iter.Next(ctx) {
		elem := iter.Value()
		am, ok := newActivityMsg(elem.Msg, elem.Entities)
		if !ok {
			continue
		}
		if cutoff > 0 && am.Date < cutoff {
			return msgs, window, nil
		}
		if len(msgs) == limit {
			window.Truncated = true // a message beyond the limit exists
			return msgs, window, nil
		}
		msgs = append(msgs, am)
	}
	if err := iter.Err(); err != nil {
		return nil, window, errors.Wrap(err, "iterate history")
	}
	window.Exhausted = true
	return msgs, window, nil
}
