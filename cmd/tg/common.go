package main

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/pflag"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
)

// messageOptions holds flags shared by the send and upload commands.
type messageOptions struct {
	html      bool
	silent    bool
	noWebpage bool
	schedule  time.Duration
	topic     int
}

// register binds the shared message flags onto the given flag set.
func (o *messageOptions) register(fs *pflag.FlagSet) {
	fs.BoolVar(&o.html, "html", false, "use HTML styling")
	fs.BoolVar(&o.silent, "silent", false,
		"send this message silently (no notifications for the receivers)")
	fs.BoolVar(&o.noWebpage, "nowebpage", false,
		"disable generation of the webpage preview")
	fs.DurationVar(&o.schedule, "schedule", 0,
		"schedule the message to be sent after this delay")
}

// registerTopic binds --topic, for commands that post a new message (not
// replies, which already land in the topic of the message they answer).
func (o *messageOptions) registerTopic(fs *pflag.FlagSet) {
	fs.IntVar(&o.topic, "topic", 0,
		"post into this forum topic (id from tg topics list; 1 or unset = General)")
}

// validateTopic rejects a negative --topic, which would otherwise post to
// General silently.
func (o *messageOptions) validateTopic() error {
	if o.topic < 0 {
		return usageError{errors.Errorf("invalid --topic %d", o.topic)}
	}
	return nil
}

// maxStdinText bounds message text read from stdin (Telegram's own limit is
// 4096 characters).
const maxStdinText = 64 << 10

// messageText returns the message text: the positional argument, or with
// --stdin the whole of standard input minus one trailing newline. Reading from
// stdin keeps secrets (login or link codes) out of the process list.
func messageText(args []string, fromStdin bool, stdin io.Reader) (string, error) {
	if !fromStdin {
		if len(args) == 0 || args[0] == "" {
			return "", usageError{errors.New("missing message text: pass it as an argument or use --stdin")}
		}
		return args[0], nil
	}
	if len(args) > 0 {
		return "", usageError{errors.New("pass the message text as an argument or with --stdin, not both")}
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxStdinText+1))
	if err != nil {
		return "", errors.Wrap(err, "read stdin")
	}
	if len(data) > maxStdinText {
		return "", usageError{errors.Errorf("message text on stdin exceeds %d bytes", maxStdinText)}
	}
	text := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if text == "" {
		return "", usageError{errors.New("empty message text on stdin")}
	}
	return text, nil
}

// targetFor returns a request builder for peer, like builderFor. When replies
// will be collected it resolves the peer first and arms c on it, so nothing the
// peer sends after the message goes out is missed.
func targetFor(
	ctx context.Context,
	m *peerManager,
	sender *message.Sender,
	peer string,
	c *replyCollector,
	wait waitOptions,
) (*message.RequestBuilder, error) {
	if !wait.enabled() {
		return builderFor(ctx, m, sender, peer)
	}
	if isSelf(peer) {
		return nil, usageError{errors.New("--wait needs another peer: Saved Messages never reply")}
	}
	input, err := resolvePeer(ctx, m, peer)
	if err != nil {
		return nil, err
	}
	c.arm(inputPeerID(input))
	return sender.To(input), nil
}

// apply mutates the request builder according to the flags and returns the
// builder together with the styled text options for the message body.
func (o *messageOptions) apply(
	r *message.RequestBuilder,
	msg string,
) (*message.Builder, []styling.StyledTextOption) {
	b := &r.Builder

	if o.silent {
		b = b.Silent()
	}
	if o.noWebpage {
		b = b.NoWebpage()
	}
	if o.schedule > 0 {
		b = b.Schedule(time.Now().Add(o.schedule))
	}
	if o.topic > generalTopicID {
		// A message replying to a topic's creation message is posted into that
		// topic; the General topic takes plain messages.
		b = b.Reply(o.topic)
	}

	option := styling.Plain(msg)
	if o.html {
		option = html.String(nil, msg)
	}

	return b, []styling.StyledTextOption{option}
}
