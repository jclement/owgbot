// Package transport defines the radio abstraction the bot runs on top of.
//
// The real implementation speaks the MeshCore companion-radio serial protocol
// (transport/meshcore); the fake implementation (transport/fake) backs the dev
// CLI and tests. Users are identified by the 6-byte public-key prefix the
// companion protocol reports for inbound DMs, rendered as 12 lowercase hex
// characters.
package transport

import (
	"context"
	"time"
)

// Message is an inbound direct message from a mesh node.
type Message struct {
	// From is the sender's public-key prefix (12 lowercase hex chars).
	From string
	// Text is the message body.
	Text string
	// SNR is the received signal-to-noise ratio in dB (0 when unknown).
	// It describes the FINAL hop into the bot's radio — for a repeated
	// message that's the last repeater's link, not the sender's.
	SNR float64
	// Hops reports the route: -1 = routed along a sender-prescribed path
	// (hop count not recorded in transit); 0 = flooded and heard directly;
	// N>0 = flooded via N recorded repeater hops.
	Hops int
	// Time is the sender's timestamp.
	Time time.Time
}

// SelfInfo describes the radio node the bot is running as.
type SelfInfo struct {
	PublicKey string // 64 hex chars
	Name      string
	// FwVer and Model describe the companion radio firmware (0/"" when
	// unknown, e.g. on the fake transport).
	FwVer int
	Model string
}

// Transport is a connection to a mesh radio.
type Transport interface {
	// Start connects and begins receiving. It returns once the transport is
	// ready; delivery continues until ctx is cancelled or Close is called.
	Start(ctx context.Context) error
	// Messages returns the inbound DM stream. Closed on shutdown.
	Messages() <-chan Message
	// Adverts returns node pubkey prefixes as adverts are heard on the mesh.
	Adverts() <-chan string
	// Send delivers a direct message to the node with the given public-key
	// prefix (12 hex chars). Blocks until the radio accepts (or rejects) it.
	Send(ctx context.Context, to string, text string) error
	// SendAdvert broadcasts a self-advert so the mesh learns this node
	// exists (flood = propagate via repeaters).
	SendAdvert(ctx context.Context, flood bool) error
	// Self reports the local node's identity (valid after Start).
	Self() SelfInfo
	// NodeName returns the advertised name for a node prefix from the
	// radio's contact list, or "" if unknown.
	NodeName(prefix string) string
	// ResolveNode turns a node name (case-insensitive) or 12-hex prefix
	// into a prefix.
	ResolveNode(nameOrPrefix string) (string, bool)
	// Close shuts the transport down.
	Close() error
}
