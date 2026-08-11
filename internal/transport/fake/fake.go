// Package fake is an in-memory transport for the dev CLI and tests.
package fake

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jclement/owgbot/internal/transport"
)

// Sent is one outbound message captured by the fake radio.
type Sent struct {
	To   string
	Text string
}

// Transport is a fake radio. Inject inbound messages with Inject; observe
// outbound messages on Outbound.
// ChannelSent is one outbound channel post captured by the fake radio.
type ChannelSent struct {
	Channel int
	Text    string
}

type Transport struct {
	msgs      chan transport.Message
	chmsgs    chan transport.ChannelMessage
	adverts   chan string
	outbound  chan Sent
	chansends chan ChannelSent

	mu          sync.Mutex
	closed      bool
	names       map[string]string // prefix → name
	advertsSent int
}

func New() *Transport {
	return &Transport{
		msgs:      make(chan transport.Message, 32),
		chmsgs:    make(chan transport.ChannelMessage, 32),
		adverts:   make(chan string, 32),
		outbound:  make(chan Sent, 128),
		chansends: make(chan ChannelSent, 32),
		names:     make(map[string]string),
	}
}

func (t *Transport) Start(ctx context.Context) error { return nil }

func (t *Transport) Messages() <-chan transport.Message { return t.msgs }

func (t *Transport) Send(ctx context.Context, to string, text string) error {
	t.outbound <- Sent{To: to, Text: text}
	return nil
}

func (t *Transport) Self() transport.SelfInfo {
	return transport.SelfInfo{
		PublicKey: "fa4e0000000000000000000000000000",
		Name:      "owgbot-dev",
		Model:     "fake radio",
	}
}

func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.closed = true
		close(t.msgs)
	}
	return nil
}

func (t *Transport) Adverts() <-chan string { return t.adverts }

func (t *Transport) ChannelMessages() <-chan transport.ChannelMessage { return t.chmsgs }

func (t *Transport) SendChannel(ctx context.Context, channel int, text string) error {
	t.chansends <- ChannelSent{Channel: channel, Text: text}
	return nil
}

// ChannelOutbound exposes the bot's channel posts (tests).
func (t *Transport) ChannelOutbound() <-chan ChannelSent { return t.chansends }

// InjectChannel simulates a message heard on a channel.
func (t *Transport) InjectChannel(channel int, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.chmsgs <- transport.ChannelMessage{Channel: channel, Text: text, SNR: -4, Time: time.Now()}
}

// SendAdvert records that a self-advert was broadcast (tests).
func (t *Transport) SendAdvert(ctx context.Context, flood bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.advertsSent++
	return nil
}

// AdvertsSent reports how many self-adverts the bot has broadcast.
func (t *Transport) AdvertsSent() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.advertsSent
}

func (t *Transport) NodeName(prefix string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.names[prefix]
}

func (t *Transport) ResolveNode(nameOrPrefix string) (string, bool) {
	if len(nameOrPrefix) == 12 && isHex(nameOrPrefix) {
		return strings.ToLower(nameOrPrefix), true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for prefix, name := range t.names {
		if strings.EqualFold(name, nameOrPrefix) {
			return prefix, true
		}
	}
	return "", false
}

// SetName registers a contact name (tests / dev).
func (t *Transport) SetName(prefix, name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.names[prefix] = name
}

// Inject delivers an inbound message to the bot as if it arrived over the
// mesh. SNR is fixed at a plausible dev value.
func (t *Transport) Inject(from, text string) {
	t.InjectAt(from, text, time.Now())
}

// InjectAt injects with an explicit sender timestamp (client retries reuse
// the original timestamp — needed to test dedup).
func (t *Transport) InjectAt(from, text string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.msgs <- transport.Message{From: from, Text: text, SNR: -2.5, Time: at}
}

// InjectAdvert simulates hearing a node advert.
func (t *Transport) InjectAdvert(prefix string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.adverts <- prefix
}

// Outbound exposes the stream of messages the bot sent.
func (t *Transport) Outbound() <-chan Sent { return t.outbound }

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
