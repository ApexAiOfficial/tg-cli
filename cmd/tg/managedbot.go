package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/tg"

	"github.com/gotd/cli/internal/atomicfile"
)

// Managed bots (Bot API 10.0, MTProto bots.createBot): a user creates a bot
// that another bot, the manager, may control. The manager needs "Bot
// Management Mode" switched on in @BotFather; it then receives a managed_bot
// update and can fetch or replace the new bot's token. These commands act as
// the creating user, so a manager bot's onboarding flow can be tested end to
// end, and let the account read or rotate the token of a bot it owns without
// the token ever reaching stdout.

// botUsernameRe is Telegram's rule for bot usernames: 5-32 characters,
// letters, digits and underscores, starting with a letter, ending in "bot".
var botUsernameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{1,28}[Bb][Oo][Tt]$`)

// validateNewBot checks a new bot's display name and username before any RPC.
func validateNewBot(name, username string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 1 || n > 64 {
		return usageError{errors.New("the bot's name must be 1-64 characters")}
	}
	if !botUsernameRe.MatchString(username) {
		return usageError{errors.Errorf("%q is not a valid bot username: 5-32 letters, digits or underscores, starting with a letter and ending in \"bot\"", username)}
	}
	return nil
}

// newBotLink is a parsed https://t.me/newbot/{manager}/{username}?name={name} link.
type newBotLink struct {
	Manager  string // "@manager"
	Username string
	Name     string
}

// parseNewBotLink parses a managed-bot creation link, with or without the scheme.
func parseNewBotLink(s string) (newBotLink, bool) {
	raw := s
	if !strings.Contains(raw, "://") {
		if !strings.HasPrefix(raw, "t.me/") && !strings.HasPrefix(raw, "telegram.me/") {
			return newBotLink{}, false
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Host != "t.me" && u.Host != "telegram.me") {
		return newBotLink{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "newbot" || parts[1] == "" || parts[2] == "" {
		return newBotLink{}, false
	}
	return newBotLink{Manager: "@" + parts[1], Username: parts[2], Name: u.Query().Get("name")}, true
}

// createdBotResult is the result of `tg create-bot` and of pressing a bot's
// "create a bot" button with `tg click`.
type createdBotResult struct {
	Bot     peerRef  `json:"bot"`
	Manager *peerRef `json:"manager,omitempty"`
	// ViaDeeplink is set when the bot was created the way a t.me/newbot link does.
	ViaDeeplink bool `json:"via_deeplink,omitempty"`
}

// MarshalText renders the new bot on one line.
func (r createdBotResult) MarshalText(w io.Writer) error {
	line := fmt.Sprintf("created %s id=%d", r.Bot.label(), r.Bot.ID)
	if r.Manager != nil {
		line += " managed by " + r.Manager.label()
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// userRef describes a user returned by an RPC.
func userRef(u *tg.User) peerRef {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	return peerRef{ID: u.ID, Type: peerUser, Name: name, Username: u.Username}
}

// createBot calls bots.createBot; manager may be nil for a bot nobody manages.
func createBot(ctx context.Context, api *tg.Client, name, username string, manager tg.InputUserClass, viaDeeplink bool) (*tg.User, error) {
	if manager == nil {
		manager = &tg.InputUserEmpty{}
	}
	res, err := api.BotsCreateBot(ctx, &tg.BotsCreateBotRequest{
		ViaDeeplink: viaDeeplink,
		Name:        strings.TrimSpace(name),
		Username:    username,
		ManagerID:   manager,
	})
	if err != nil {
		return nil, errors.Wrap(err, "bots.createBot")
	}
	u, ok := res.(*tg.User)
	if !ok || !u.Bot {
		return nil, errors.Errorf("bots.createBot returned %T, not a bot", res)
	}
	return u, nil
}

func (a *app) newCreateBotCmd() *cobra.Command {
	var (
		name, username, manager string
		checkOnly               bool
	)
	cmd := &cobra.Command{
		Use:     "create-bot [newbot-link]",
		Short:   "Create a bot, optionally managed by another bot",
		GroupID: groupMessaging,
		Long: `Create a Telegram bot owned by this account, without @BotFather's chat.

With --manager, or a https://t.me/newbot/<manager>/<username>?name=<name> link,
the new bot is managed by that bot: the manager (which must have Bot Management
Mode on in @BotFather) receives a managed_bot update and can fetch the new
bot's token with getManagedBotToken. A link is created the way Telegram's app
does when the link is opened (via_deeplink), so a manager's onboarding link can
be tested exactly. Flags override the link's suggestions.

The new bot's token is never printed; see ` + "`tg bot-token`" + `. --check only
asks Telegram whether the username is free.`,
		Example: `  tg create-bot "https://t.me/newbot/MyManagerBot/MyShopBot?name=My+Shop"
  tg create-bot --name "My Shop" --username MyShopBot --manager @MyManagerBot -o json
  tg create-bot --username MyShopBot --check`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			viaDeeplink := false
			if len(args) == 1 {
				link, ok := parseNewBotLink(args[0])
				if !ok {
					return usageError{errors.Errorf("%q is not a t.me/newbot/<manager>/<username> link", args[0])}
				}
				viaDeeplink = true
				if manager == "" {
					manager = link.Manager
				}
				if username == "" {
					username = link.Username
				}
				if name == "" {
					name = link.Name
				}
			}
			if checkOnly {
				if !botUsernameRe.MatchString(username) {
					return validateNewBot("x", username)
				}
			} else if err := validateNewBot(name, username); err != nil {
				return err
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				if checkOnly {
					free, err := api.BotsCheckUsername(ctx, username)
					if err != nil && !isUsernameTaken(err) {
						return errors.Wrap(err, "bots.checkUsername")
					}
					return a.printer.Emit(usernameCheckResult{Username: username, Available: err == nil && free})
				}
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				var (
					mgrInput tg.InputUserClass
					mgrRef   *peerRef
				)
				if manager != "" {
					mgr, err := resolveBot(ctx, m, manager)
					if err != nil {
						return err
					}
					mgrInput = mgr.InputUser()
					ref := describeManagedPeer(mgr)
					mgrRef = &ref
				}
				u, err := createBot(ctx, api, name, username, mgrInput, viaDeeplink)
				if err != nil {
					return err
				}
				if err := m.Apply(ctx, []tg.UserClass{u}, nil); err != nil {
					return errors.Wrap(err, "cache new bot")
				}
				return a.printer.Emit(createdBotResult{Bot: userRef(u), Manager: mgrRef, ViaDeeplink: viaDeeplink})
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&name, "name", "", "the new bot's display name (1-64 characters)")
	fs.StringVar(&username, "username", "", "the new bot's username, ending in \"bot\"")
	fs.StringVar(&manager, "manager", "", "the bot that will manage the new bot")
	fs.BoolVar(&checkOnly, "check", false, "only check whether the username is available")
	return cmd
}

// isUsernameTaken reports the errors bots.checkUsername uses for a username
// that cannot be had.
func isUsernameTaken(err error) bool {
	for _, code := range []string{"USERNAME_OCCUPIED", "USERNAME_PURCHASE_AVAILABLE", "USERNAME_INVALID"} {
		if strings.Contains(err.Error(), code) {
			return true
		}
	}
	return false
}

// usernameCheckResult is the result of `tg create-bot --check`.
type usernameCheckResult struct {
	Username  string `json:"username"`
	Available bool   `json:"available"`
}

// MarshalText renders the check on one line.
func (r usernameCheckResult) MarshalText(w io.Writer) error {
	state := "taken or invalid"
	if r.Available {
		state = "available"
	}
	_, err := fmt.Fprintf(w, "@%s: %s\n", r.Username, state)
	return err
}

// botTokenResult is the result of `tg bot-token`: where the token went, never the token.
type botTokenResult struct {
	Bot     peerRef `json:"bot"`
	Out     string  `json:"out"`
	Revoked bool    `json:"revoked,omitempty"`
}

// MarshalText renders the result on one line.
func (r botTokenResult) MarshalText(w io.Writer) error {
	verb := "saved"
	if r.Revoked {
		verb = "revoked the old token; saved the new one"
	}
	_, err := fmt.Fprintf(w, "%s: %s to %s (mode 0600)\n", r.Bot.label(), verb, r.Out)
	return err
}

// writeBotToken writes a token to a new owner-only file; it never replaces a file.
func writeBotToken(path, token string) error {
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("Telegram returned an unusable token")
	}
	if err := atomicfile.Create(path, []byte(token+"\n"), 0o600); err != nil {
		return errors.Wrap(err, "write token")
	}
	return nil
}

func (a *app) newBotTokenCmd() *cobra.Command {
	var (
		out         string
		revoke, yes bool
	)
	cmd := &cobra.Command{
		Use:     "bot-token <bot>",
		Short:   "Save a bot's token to an owner-only file",
		GroupID: groupMessaging,
		Long: `Export the token of a bot this account owns (bots.exportBotToken) into a new
file created with mode 0600. The token is never printed, so it stays out of
terminals, logs and agent transcripts. The file must not exist yet.

--revoke replaces the token: the old one stops working at once, so it needs --yes.`,
		Example: `  tg bot-token @MyShopBot --out ./shop.token
  tg bot-token @MyShopBot --out ./shop.token --revoke --yes`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return usageError{errors.New("--out is required: the token is only ever written to a file")}
			}
			if revoke && !yes {
				return usageError{errors.New("--revoke stops the current token at once; pass --yes to confirm")}
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				bot, err := resolveBot(ctx, m, args[0])
				if err != nil {
					return err
				}
				tok, err := api.BotsExportBotToken(ctx, &tg.BotsExportBotTokenRequest{Bot: bot.InputUser(), Revoke: revoke})
				if err != nil {
					return errors.Wrap(err, "bots.exportBotToken")
				}
				if err := writeBotToken(out, tok.Token); err != nil {
					return err
				}
				return a.printer.Emit(botTokenResult{Bot: describeManagedPeer(bot), Out: out, Revoked: revoke})
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&out, "out", "", "new file to write the token to (mode 0600)")
	fs.BoolVar(&revoke, "revoke", false, "replace the token; the old one stops working")
	fs.BoolVar(&yes, "yes", false, "confirm --revoke")
	return cmd
}

// botAccessResult is the result of `tg bot-access`.
type botAccessResult struct {
	Bot        peerRef   `json:"bot"`
	Restricted bool      `json:"restricted"`
	Users      []peerRef `json:"users,omitempty"`
}

// MarshalText renders the access settings.
func (r botAccessResult) MarshalText(w io.Writer) error {
	line := fmt.Sprintf("%s: open to everyone", r.Bot.label())
	if r.Restricted {
		names := make([]string, 0, len(r.Users))
		for _, u := range r.Users {
			names = append(names, u.label())
		}
		line = fmt.Sprintf("%s: restricted to its owner", r.Bot.label())
		if len(names) > 0 {
			line += " and " + strings.Join(names, ", ")
		}
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// newBotAccessResult converts bots.accessSettings.
func newBotAccessResult(bot peerRef, s *tg.BotsAccessSettings) botAccessResult {
	res := botAccessResult{Bot: bot, Restricted: s.Restricted}
	for _, u := range s.AddUsers {
		if user, ok := u.(*tg.User); ok {
			res.Users = append(res.Users, userRef(user))
		}
	}
	return res
}

func (a *app) newBotAccessCmd() *cobra.Command {
	var (
		restrict, open bool
		users          []string
	)
	cmd := &cobra.Command{
		Use:     "bot-access <bot>",
		Short:   "Show or change who may use a bot",
		GroupID: groupMessaging,
		Long: `Show a bot's access settings (bots.getAccessSettings). With --restrict, only
the bot's owner and up to 10 --user accounts may use it; --open lets everyone
use it again (bots.editAccessSettings).`,
		Example: `  tg bot-access @MyShopBot
  tg bot-access @MyShopBot --restrict --user @alice --user @bob
  tg bot-access @MyShopBot --open`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case restrict && open:
				return usageError{errors.New("use only one of --restrict and --open")}
			case len(users) > 10:
				return usageError{errors.New("at most 10 --user accounts besides the owner")}
			case len(users) > 0 && !restrict:
				return usageError{errors.New("--user needs --restrict")}
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				bot, err := resolveBot(ctx, m, args[0])
				if err != nil {
					return err
				}
				if restrict || open {
					req := &tg.BotsEditAccessSettingsRequest{Restricted: restrict, Bot: bot.InputUser()}
					if len(users) > 0 {
						add := make([]tg.InputUserClass, 0, len(users))
						for _, arg := range users {
							p, err := resolveManagedPeer(ctx, m, arg)
							if err != nil {
								return err
							}
							u, ok := p.(interface{ InputUser() tg.InputUserClass })
							if !ok {
								return usageError{errors.Errorf("%s is not a user", arg)}
							}
							add = append(add, u.InputUser())
						}
						req.SetAddUsers(add)
					}
					if _, err := api.BotsEditAccessSettings(ctx, req); err != nil {
						return errors.Wrap(err, "bots.editAccessSettings")
					}
				}
				s, err := api.BotsGetAccessSettings(ctx, bot.InputUser())
				if err != nil {
					return errors.Wrap(err, "bots.getAccessSettings")
				}
				return a.printer.Emit(newBotAccessResult(describeManagedPeer(bot), s))
			})
		},
	}
	fs := cmd.Flags()
	fs.BoolVar(&restrict, "restrict", false, "allow only the owner and the --user accounts")
	fs.BoolVar(&open, "open", false, "allow everyone again")
	fs.StringArrayVar(&users, "user", nil, "account allowed besides the owner (repeatable, with --restrict)")
	return cmd
}
