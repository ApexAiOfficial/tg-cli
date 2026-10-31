package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/gotd/cli/internal/atomicfile"
)

// webAppDefaultPlatform is the client the session is requested as.
const webAppDefaultPlatform = "tdesktop"

// What opened a Mini App.
const (
	webAppSourceMenu   = "menu_button"
	webAppSourceButton = "button"
	webAppSourceURL    = "url"
	webAppSourceMain   = "main"
)

// webAppResult is the result of `tg webapp`. It never carries the signed
// launch data unless --reveal asks for it: that data is a credential for the
// Mini App's user until the app's own expiry.
type webAppResult struct {
	Peer peerRef `json:"peer"`
	// Source is what opened the app: "menu_button", "button", "url" or "main".
	Source string `json:"source"`
	// URL is the page, without its signed fragment.
	URL      string `json:"url"`
	Platform string `json:"platform,omitempty"`
	Version  string `json:"version,omitempty"`
	// Signed is true when Telegram returned launch data (tgWebAppData).
	Signed     bool   `json:"signed"`
	UserID     int64  `json:"user_id,omitempty"`
	AuthDate   int64  `json:"auth_date,omitempty"`
	QueryID    string `json:"query_id,omitempty"`
	StartParam string `json:"start_param,omitempty"`
	// OutFile is where --out wrote the signed URL and launch data.
	OutFile string `json:"out_file,omitempty"`
	// SignedURL and InitData are set only with --reveal.
	SignedURL string `json:"signed_url,omitempty"`
	InitData  string `json:"init_data,omitempty"`
}

// MarshalText renders the session without its credential.
func (r webAppResult) MarshalText(w io.Writer) error {
	lines := []string{fmt.Sprintf("%s Mini App (%s): %s", r.Peer.label(), r.Source, r.URL)}
	if r.Signed {
		line := fmt.Sprintf("signed launch data: user %d", r.UserID)
		if r.AuthDate != 0 {
			line += ", issued " + time.Unix(r.AuthDate, 0).UTC().Format(time.RFC3339)
		}
		lines = append(lines, line)
	} else {
		lines = append(lines, "no signed launch data returned")
	}
	if r.QueryID != "" {
		lines = append(lines, "query id: "+r.QueryID)
	}
	if r.OutFile != "" {
		lines = append(lines, "written to "+r.OutFile)
	}
	if r.SignedURL != "" {
		lines = append(lines, "signed url: "+r.SignedURL)
	}
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}

// webAppLaunch is the launch data parsed from a web view URL.
type webAppLaunch struct {
	Page       string // the URL without its fragment
	Version    string
	Platform   string
	InitData   string // the raw tgWebAppData value, a credential
	UserID     int64
	AuthDate   int64
	QueryID    string
	StartParam string
}

// parseWebAppURL splits a web view URL into its page and launch data. Telegram
// puts the launch data in the fragment, so it never reaches the app's server
// in the request line.
func parseWebAppURL(raw string) (webAppLaunch, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// Parse errors quote the input, which here holds the credential.
		return webAppLaunch{}, errors.New("telegram returned a web view URL that does not parse")
	}
	fragment := u.EscapedFragment() // Fragment is already decoded once, and ParseQuery decodes it
	u.Fragment, u.RawFragment = "", ""
	launch := webAppLaunch{Page: u.String()}
	values, err := url.ParseQuery(fragment)
	if err != nil {
		return webAppLaunch{}, errors.New("telegram returned a web view fragment that does not parse")
	}
	launch.Version = values.Get("tgWebAppVersion")
	launch.Platform = values.Get("tgWebAppPlatform")
	launch.InitData = values.Get("tgWebAppData")
	if launch.InitData == "" {
		return launch, nil
	}
	data, err := url.ParseQuery(launch.InitData)
	if err != nil {
		return webAppLaunch{}, errors.New("telegram returned launch data that does not parse")
	}
	launch.AuthDate, _ = strconv.ParseInt(data.Get("auth_date"), 10, 64)
	launch.QueryID = data.Get("query_id")
	launch.StartParam = data.Get("start_param")
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(data.Get("user")), &user); err == nil {
		launch.UserID = user.ID
	}
	return launch, nil
}

// webAppSecret is what --out writes: everything a browser or test needs.
type webAppSecret struct {
	URL      string `json:"url"`
	InitData string `json:"init_data"`
	Page     string `json:"page"`
}

// writeWebAppSecret writes the signed session to path, owner-only.
func writeWebAppSecret(path, signedURL string, launch webAppLaunch) error {
	data, err := json.MarshalIndent(webAppSecret{URL: signedURL, InitData: launch.InitData, Page: launch.Page}, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode session")
	}
	if err := atomicfile.Create(path, append(data, '\n'), 0o600); err != nil {
		return errors.Wrap(err, "write session")
	}
	return nil
}

// webAppButtonURL returns the Mini App URL of a keyboard button, and whether
// it is the kind a client opens with messages.requestSimpleWebView.
func webAppButtonURL(b tg.KeyboardButtonClass) (target string, simple, ok bool) {
	switch v := b.(type) {
	case *tg.KeyboardButtonWebView:
		return v.URL, false, true
	case *tg.KeyboardButtonSimpleWebView:
		return v.URL, true, true
	}
	return "", false, false
}

// webAppOptions are the flags of `tg webapp`.
type webAppOptions struct {
	url        string
	text       string
	main       bool
	startParam string
	platform   string
	out        string
	reveal     bool
}

func (o webAppOptions) validate() error {
	if (o.url != "" && o.text != "") || (o.main && (o.url != "" || o.text != "")) {
		return usageError{errors.New("use only one of --url, --text and --main")}
	}
	return nil
}

// webAppTarget is what to open.
type webAppTarget struct {
	source string
	url    string
	simple bool // a client opens it with messages.requestSimpleWebView
}

// resolveWebAppTarget finds the page the options name.
func resolveWebAppTarget(ctx context.Context, api *tg.Client, bot peers.User, name string, o webAppOptions) (webAppTarget, error) {
	switch {
	case o.main:
		return webAppTarget{source: webAppSourceMain}, nil
	case o.url != "":
		return webAppTarget{source: webAppSourceURL, url: o.url}, nil
	case o.text != "":
		msg, err := keyboardMessage(ctx, api, bot.InputPeer(), 0)
		if err != nil {
			return webAppTarget{}, err
		}
		btn, err := selectButtonByText(keyboardButtons(msg.ReplyMarkup), o.text)
		if err != nil {
			return webAppTarget{}, err
		}
		target, simple, ok := webAppButtonURL(btn.raw)
		if !ok {
			return webAppTarget{}, usageError{errors.Errorf("button %q does not open a Mini App", o.text)}
		}
		return webAppTarget{source: webAppSourceButton, url: target, simple: simple}, nil
	}
	full, err := bot.FullRaw(ctx)
	if err != nil {
		return webAppTarget{}, errors.Wrap(err, "users.getFullUser")
	}
	info, ok := full.GetBotInfo()
	if !ok {
		return webAppTarget{}, notFoundf("%s has no bot info", name)
	}
	menu, ok := info.MenuButton.(*tg.BotMenuButton)
	if !ok {
		return webAppTarget{}, notFoundf("%s has no Mini App menu button", name)
	}
	return webAppTarget{source: webAppSourceMenu, url: menu.URL}, nil
}

// requestWebApp asks Telegram to open the target, with the call a client uses for it.
func requestWebApp(ctx context.Context, api *tg.Client, bot peers.User, t webAppTarget, o webAppOptions) (*tg.WebViewResultURL, error) {
	peer, user := bot.InputPeer(), bot.InputUser()
	switch {
	case t.source == webAppSourceMain:
		res, err := api.MessagesRequestMainWebView(ctx, &tg.MessagesRequestMainWebViewRequest{
			Peer: peer, Bot: user, StartParam: o.startParam, Platform: o.platform,
		})
		return res, errors.Wrap(err, "messages.requestMainWebView")
	case t.simple:
		res, err := api.MessagesRequestSimpleWebView(ctx, &tg.MessagesRequestSimpleWebViewRequest{
			Bot: user, URL: t.url, Platform: o.platform,
		})
		return res, errors.Wrap(err, "messages.requestSimpleWebView")
	}
	res, err := api.MessagesRequestWebView(ctx, &tg.MessagesRequestWebViewRequest{
		FromBotMenu: t.source == webAppSourceMenu,
		Peer:        peer, Bot: user, URL: t.url, StartParam: o.startParam, Platform: o.platform,
	})
	return res, errors.Wrap(err, "messages.requestWebView")
}

// newWebAppResult builds the result from what Telegram returned, and writes --out.
func newWebAppResult(bot peers.User, source, signedURL string, o webAppOptions) (webAppResult, error) {
	launch, err := parseWebAppURL(signedURL)
	if err != nil {
		return webAppResult{}, err
	}
	res := webAppResult{
		Peer: describeManagedPeer(bot), Source: source, URL: launch.Page,
		Platform: launch.Platform, Version: launch.Version, Signed: launch.InitData != "",
		UserID: launch.UserID, AuthDate: launch.AuthDate, QueryID: launch.QueryID, StartParam: launch.StartParam,
	}
	if o.out != "" {
		if err := writeWebAppSecret(o.out, signedURL, launch); err != nil {
			return webAppResult{}, err
		}
		res.OutFile = o.out
	}
	if o.reveal {
		res.SignedURL, res.InitData = signedURL, launch.InitData
	}
	return res, nil
}

func (a *app) newWebAppCmd() *cobra.Command {
	var o webAppOptions
	cmd := &cobra.Command{
		Use:     "webapp <bot>",
		Short:   "Open a bot's Mini App and get its signed launch data",
		GroupID: groupMessaging,
		Long: `Ask Telegram to open a bot's Mini App, the way a client does, and report the
session: the page and the identity Telegram signed for this account. A real
signed session lets a test drive the app's API or load it in a browser with no
Telegram client:

  Authorization: tma <init_data>     for the app's API
  the signed URL in a browser        window.Telegram.WebApp reads its fragment

By default this opens the bot's menu button. --text opens a web app button from
the bot's newest keyboard, --url a URL of your choice, --main the bot's main
Mini App (with a --start-param if it takes one).

The launch data is a credential: anyone holding it can act as this account in
that Mini App until the app's own expiry. It is printed only with --reveal, and
--out writes it to an owner-only file. Opening an app reveals this account's id,
name and language to the bot's server, as any client does.`,
		Example: `  tg webapp @mybot
  tg webapp @mybot --out session.json && jq -r .url session.json
  tg webapp @mybot --text "Open store" -o json
  tg webapp @mybot --main --start-param promo`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.validate(); err != nil {
				return err
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
				target, err := resolveWebAppTarget(ctx, api, bot, args[0], o)
				if err != nil {
					return err
				}
				view, err := requestWebApp(ctx, api, bot, target, o)
				if err != nil {
					return err
				}
				res, err := newWebAppResult(bot, target.source, view.URL, o)
				if err != nil {
					return err
				}
				return a.printer.Emit(res)
			})
		},
	}

	fs := cmd.Flags()
	fs.StringVar(&o.url, "url", "", "open this URL as the bot's Mini App")
	fs.StringVar(&o.text, "text", "", "open the web app button with this label")
	fs.BoolVar(&o.main, "main", false, "open the bot's main Mini App")
	fs.StringVar(&o.startParam, "start-param", "", "start parameter passed to the app")
	fs.StringVar(&o.platform, "platform", webAppDefaultPlatform, "client platform to report (tdesktop, android, ios, web)")
	fs.StringVar(&o.out, "out", "", "write the signed URL and launch data to this owner-only file")
	fs.BoolVar(&o.reveal, "reveal", false, "print the signed URL and launch data (a credential)")

	return cmd
}
