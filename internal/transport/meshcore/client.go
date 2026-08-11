// Package meshcore implements transport.Transport over the MeshCore
// companion-radio serial protocol (Heltec V3 etc. on USB serial).
package meshcore

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"

	"github.com/jclement/owgbot/internal/transport"
)

const (
	cmdTimeout       = 5 * time.Second
	reconnectMin     = 2 * time.Second
	reconnectMax     = 60 * time.Second
	syncPollInterval = 30 * time.Second // safety-net poll in case a MSG_WAITING push is missed
)

// Client is a MeshCore companion-radio transport over a serial port.
type Client struct {
	portName string
	baud     int
	log      *slog.Logger

	msgs    chan transport.Message
	chmsgs  chan transport.ChannelMessage
	adverts chan string

	cmdMu sync.Mutex // serializes command round-trips (protocol allows one in flight)
	mu    sync.Mutex // guards port, self, resp, contacts
	port  serial.Port
	self  transport.SelfInfo
	resp  chan []byte // responses to the in-flight command
	kick  chan struct{}
	close sync.Once
	done  chan struct{}

	contacts        map[string]string // prefix → advertised name
	channels        map[int]string    // slot → channel name
	contactsFetched time.Time
}

// New creates a client for the given serial port (e.g. /dev/ttyUSB0).
func New(portName string, baud int, log *slog.Logger) *Client {
	return &Client{
		portName: portName,
		baud:     baud,
		log:      log.With("component", "meshcore"),
		msgs:     make(chan transport.Message, 32),
		chmsgs:   make(chan transport.ChannelMessage, 32),
		adverts:  make(chan string, 32),
		kick:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		contacts: make(map[string]string),
		channels: make(map[int]string),
	}
}

// Start connects to the radio and launches the receive/sync loops. The first
// connection must succeed; later disconnects are retried with backoff.
func (c *Client) Start(ctx context.Context) error {
	if err := c.connect(ctx); err != nil {
		return err
	}
	go c.run(ctx)
	return nil
}

func (c *Client) Messages() <-chan transport.Message { return c.msgs }

func (c *Client) Adverts() <-chan string { return c.adverts }

func (c *Client) ChannelMessages() <-chan transport.ChannelMessage { return c.chmsgs }

// SendChannel posts a message to a channel slot.
func (c *Client) SendChannel(ctx context.Context, channel int, text string) error {
	if len(text) > 130 {
		text = text[:130] // channel messages cap lower than DMs
	}
	f, err := c.roundTrip(ctx, buildSendChannelMsg(byte(channel), text, time.Now()))
	if err != nil {
		return err
	}
	if f[0] == respErr {
		return fmt.Errorf("meshcore: channel send rejected")
	}
	return nil
}

func (c *Client) NodeName(prefix string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.contacts[prefix]
}

func (c *Client) ResolveNode(nameOrPrefix string) (string, bool) {
	if b, err := prefixToBytes(nameOrPrefix); err == nil && len(b) == 6 {
		return strings.ToLower(nameOrPrefix), true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for prefix, name := range c.contacts {
		if strings.EqualFold(name, nameOrPrefix) {
			return prefix, true
		}
	}
	return "", false
}

func (c *Client) Self() transport.SelfInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.self
}

// Send delivers a DM to the node with the given pubkey prefix (12 hex chars).
func (c *Client) Send(ctx context.Context, to string, text string) error {
	prefix, err := prefixToBytes(to)
	if err != nil {
		return err
	}
	if len(text) > maxTextBytes {
		text = text[:maxTextBytes]
	}
	f, err := c.roundTrip(ctx, buildSendTxtMsg(prefix, text, 0, time.Now()))
	if err != nil {
		return err
	}
	switch f[0] {
	case respSent, respOK:
		return nil
	case respErr:
		code := byte(0)
		if len(f) > 1 {
			code = f[1]
		}
		return fmt.Errorf("meshcore: send rejected (err %d)", code)
	default:
		return fmt.Errorf("meshcore: unexpected send response 0x%02x", f[0])
	}
}

// SendAdvert broadcasts CMD_SEND_SELF_ADVERT.
func (c *Client) SendAdvert(ctx context.Context, flood bool) error {
	f, err := c.roundTrip(ctx, buildSendSelfAdvert(flood))
	if err != nil {
		return err
	}
	if f[0] == respErr {
		return fmt.Errorf("meshcore: advert rejected")
	}
	return nil
}

func (c *Client) Close() error {
	c.close.Do(func() { close(c.done) })
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.port != nil {
		return c.port.Close()
	}
	return nil
}

// connect opens the serial port and performs the session handshake:
// APP_START → SELF_INFO, then SET_DEVICE_TIME so message timestamps are sane.
func (c *Client) connect(ctx context.Context) error {
	port, err := serial.Open(c.portName, &serial.Mode{BaudRate: c.baud})
	if err != nil {
		return fmt.Errorf("meshcore: open %s: %w", c.portName, err)
	}

	c.mu.Lock()
	c.port = port
	c.resp = make(chan []byte, 8)
	c.mu.Unlock()

	go c.readLoop(port)

	// Declare protocol version 3 first — without this the firmware answers
	// with V1 message frames, which carry no SNR.
	var fwVer int
	var model string
	if f, err := c.roundTrip(ctx, buildDeviceQuery()); err != nil {
		c.log.Warn("device query failed; SNR may be unavailable", "err", err)
	} else if fwVer, model = parseDeviceInfo(f); fwVer > 0 {
		c.log.Info("device", "fw_ver", fwVer, "model", model)
	}

	f, err := c.roundTrip(ctx, buildAppStart("owgbot"))
	if err != nil {
		port.Close()
		return fmt.Errorf("meshcore: APP_START: %w", err)
	}
	si, err := parseSelfInfo(f)
	if err != nil {
		port.Close()
		return err
	}
	c.mu.Lock()
	c.self = transport.SelfInfo{
		PublicKey: hex.EncodeToString(si.publicKey),
		Name:      si.name,
		FwVer:     fwVer,
		Model:     model,
	}
	c.mu.Unlock()
	c.log.Info("connected", "node", si.name, "pubkey", hex.EncodeToString(si.publicKey)[:12])

	if _, err := c.roundTrip(ctx, buildSetDeviceTime(time.Now())); err != nil {
		c.log.Warn("set device time failed", "err", err)
	}
	if err := c.syncContacts(); err != nil {
		c.log.Warn("contacts sync failed", "err", err)
	}
	c.syncChannels(ctx)
	// Drain anything queued while we were offline.
	c.kickSync()
	return nil
}

// syncChannels reads the names of all 8 channel slots (unconfigured slots
// error or come back empty — both fine).
func (c *Client) syncChannels(ctx context.Context) {
	fresh := make(map[int]string)
	for slot := 0; slot < 8; slot++ {
		f, err := c.roundTrip(ctx, buildGetChannel(byte(slot)))
		if err != nil || len(f) == 0 || f[0] != respChannelInfo {
			continue
		}
		if idx, name, err := parseChannelInfo(f); err == nil && name != "" {
			fresh[idx] = name
		}
	}
	c.mu.Lock()
	c.channels = fresh
	c.mu.Unlock()
	c.log.Debug("channels synced", "count", len(fresh))
}

func (c *Client) ChannelName(channel int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channels[channel]
}

// SetChannel provisions a channel slot on the radio (name + 16-byte secret).
func (c *Client) SetChannel(ctx context.Context, channel int, name string, secret []byte) error {
	if len(secret) != 16 {
		return fmt.Errorf("meshcore: channel secret must be 16 bytes (got %d)", len(secret))
	}
	f, err := c.roundTrip(ctx, buildSetChannel(byte(channel), name, secret))
	if err != nil {
		return err
	}
	if f[0] == respErr {
		return fmt.Errorf("meshcore: set channel rejected")
	}
	c.mu.Lock()
	c.channels[channel] = name
	c.mu.Unlock()
	return nil
}

// syncContacts refreshes the prefix→name map from the radio's contact list.
// CMD_GET_CONTACTS answers with a stream of frames (START, CONTACT×N, END),
// so it holds the command lock and reads until the END marker.
func (c *Client) syncContacts() error {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()

	c.mu.Lock()
	port, resp := c.port, c.resp
	if port == nil {
		c.mu.Unlock()
		return fmt.Errorf("meshcore: not connected")
	}
	err := writeFrame(port, buildGetContacts())
	c.mu.Unlock()
	if err != nil {
		return err
	}

	fresh := make(map[string]string)
	deadline := time.After(cmdTimeout)
	for {
		select {
		case f := <-resp:
			switch f[0] {
			case respContactsStart:
				// header; count follows but we just read to END
			case respContact:
				prefix, name, err := parseContact(f)
				if err != nil {
					c.log.Warn("bad contact frame", "err", err)
					continue
				}
				fresh[prefix] = name
			case respEndOfContacts:
				c.mu.Lock()
				c.contacts = fresh
				c.contactsFetched = time.Now()
				c.mu.Unlock()
				c.log.Debug("contacts synced", "count", len(fresh))
				return nil
			default:
				c.log.Debug("ignoring frame during contact sync", "code", fmt.Sprintf("0x%02x", f[0]))
			}
		case <-deadline:
			return fmt.Errorf("meshcore: contact sync timed out")
		}
	}
}

// run owns reconnection and the message-sync loop.
func (c *Client) run(ctx context.Context) {
	defer close(c.msgs)
	ticker := time.NewTicker(syncPollInterval)
	defer ticker.Stop()

	backoff := reconnectMin
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-c.kick:
			if err := c.syncMessages(ctx); err != nil {
				c.log.Warn("sync failed; reconnecting", "err", err)
				if !c.reconnect(ctx, &backoff) {
					return
				}
			} else {
				backoff = reconnectMin
			}
		case <-ticker.C:
			c.kickSync()
		}
	}
}

func (c *Client) reconnect(ctx context.Context, backoff *time.Duration) bool {
	c.mu.Lock()
	if c.port != nil {
		c.port.Close()
		c.port = nil
	}
	c.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-c.done:
			return false
		case <-time.After(*backoff):
		}
		if err := c.connect(ctx); err != nil {
			c.log.Warn("reconnect failed", "err", err, "retry_in", *backoff)
			*backoff = min(*backoff*2, reconnectMax)
			continue
		}
		*backoff = reconnectMin
		return true
	}
}

// readLoop reads frames off the serial port, routing pushes to the sync loop
// and everything else to the in-flight command's response channel.
func (c *Client) readLoop(port serial.Port) {
	r := bufio.NewReader(port)
	for {
		f, err := readFrame(r)
		if err != nil {
			c.log.Debug("read loop ended", "err", err)
			return
		}
		if len(f) == 0 {
			continue
		}
		if isPush(f[0]) {
			c.handlePush(f)
			continue
		}
		c.mu.Lock()
		resp := c.resp
		c.mu.Unlock()
		select {
		case resp <- f:
		default:
			c.log.Debug("dropping unexpected response frame", "code", fmt.Sprintf("0x%02x", f[0]))
		}
	}
}

func (c *Client) handlePush(f []byte) {
	switch f[0] {
	case pushMsgWaiting:
		c.kickSync()
	case pushSendConfirmed:
		// Delivery ack — informational only for now.
		c.log.Debug("send confirmed")
	case pushAdvert:
		if len(f) < 33 {
			return
		}
		prefix := hex.EncodeToString(f[1:7])
		c.log.Debug("advert", "pubkey", prefix)
		select {
		case c.adverts <- prefix:
		default:
		}
		// A new neighbor may have been auto-added to the contact list;
		// refresh names occasionally (from a goroutine — this runs on the
		// read loop, which syncContacts needs alive to see responses).
		c.mu.Lock()
		stale := time.Since(c.contactsFetched) > 5*time.Minute
		if stale {
			c.contactsFetched = time.Now() // claim it so only one refresh runs
		}
		c.mu.Unlock()
		if stale {
			go func() {
				if err := c.syncContacts(); err != nil {
					c.log.Debug("contact refresh failed", "err", err)
				}
			}()
		}
	}
}

func (c *Client) kickSync() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// syncMessages pulls queued messages until the radio reports no more.
func (c *Client) syncMessages(ctx context.Context) error {
	for {
		f, err := c.roundTrip(ctx, buildSyncNextMessage())
		if err != nil {
			return err
		}
		switch f[0] {
		case respNoMoreMsgs, respOK:
			return nil
		case respContactMsgRecv, respContactMsgV3:
			m, err := parseContactMsg(f)
			if err != nil {
				c.log.Warn("bad contact msg frame", "err", err)
				continue
			}
			msg := transport.Message{
				From: hex.EncodeToString(m.fromPrefix),
				Text: m.text,
				SNR:  m.snr,
				Hops: m.hops,
				Time: m.timestamp,
			}
			select {
			case c.msgs <- msg:
			default:
				c.log.Warn("inbound queue full; dropping message", "from", msg.From)
			}
		case respChannelMsgRecv, respChannelMsgV3:
			m, err := parseChannelMsg(f)
			if err != nil {
				c.log.Warn("bad channel msg frame", "err", err)
				continue
			}
			select {
			case c.chmsgs <- transport.ChannelMessage{
				Channel: m.channel, Text: m.text, SNR: m.snr, Time: m.timestamp,
			}:
			default:
			}
		default:
			c.log.Debug("ignoring sync frame", "code", fmt.Sprintf("0x%02x", f[0]))
		}
	}
}

// roundTrip sends one command and waits for its response. The companion
// protocol is strictly one command in flight at a time.
func (c *Client) roundTrip(ctx context.Context, cmd []byte) ([]byte, error) {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()

	c.mu.Lock()
	port, resp := c.port, c.resp
	if port == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("meshcore: not connected")
	}
	// Drain any stale response left over from a timed-out command.
	for {
		select {
		case <-resp:
			continue
		default:
		}
		break
	}
	err := writeFrame(port, cmd)
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("meshcore: write: %w", err)
	}
	select {
	case f := <-resp:
		return f, nil
	case <-time.After(cmdTimeout):
		return nil, fmt.Errorf("meshcore: command 0x%02x timed out", cmd[0])
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
