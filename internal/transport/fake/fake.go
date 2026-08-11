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
type Transport struct {
	msgs     chan transport.Message
	adverts  chan string
	outbound chan Sent

	mu     sync.Mutex
	closed bool
	names  map[string]string // prefix → name
}

func New() *Transport {
	return &Transport{
		msgs:     make(chan transport.Message, 32),
		adverts:  make(chan string, 32),
		outbound: make(chan Sent, 128),
		names:    make(map[string]string),
	}
}

func (t *Transport) Start(ctx context.Context) error { return nil }

func (t *Transport) Messages() <-chan transport.Message { return t.msgs }

func (t *Transport) Send(ctx context.Context, to string, text string) error {
	t.outbound <- Sent{To: to, Text: text}
	return nil
}

func (t *Transport) Self() transport.SelfInfo {
	return transport.SelfInfo{PublicKey: "fa4e0000000000000000000000000000", Name: "owgbot-dev"}
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
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.msgs <- transport.Message{From: from, Text: text, SNR: -2.5, Time: time.Now()}
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
