package meshcore

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// MeshCore companion-radio protocol codes.
// Reference: https://github.com/meshcore-dev/MeshCore docs/companion_protocol.md
const (
	cmdAppStart        = 0x01
	cmdSendTxtMsg      = 0x02
	cmdGetContacts     = 0x04
	cmdSetDeviceTime   = 0x06
	cmdSendSelfAdvert  = 0x07
	cmdSyncNextMessage = 0x0A
	cmdDeviceQuery     = 0x16

	respOK             = 0x00
	respErr            = 0x01
	respContactsStart  = 0x02
	respContact        = 0x03
	respEndOfContacts  = 0x04
	respSelfInfo       = 0x05
	respSent           = 0x06
	respContactMsgRecv = 0x07
	respChannelMsgRecv = 0x08
	respNoMoreMsgs     = 0x0A
	respDeviceInfo     = 0x0D
	respContactMsgV3   = 0x10
	respChannelMsgV3   = 0x11

	pushAdvert        = 0x80
	pushPathUpdated   = 0x81
	pushSendConfirmed = 0x82
	pushMsgWaiting    = 0x83
)

// maxTextBytes is the protocol limit for a DM body (CMD_SEND_TXT_MSG).
const maxTextBytes = 160

// isPush reports whether a frame code is an unsolicited push notification.
func isPush(code byte) bool { return code >= 0x80 }

// buildAppStart builds CMD_APP_START: [0x01][7 reserved][app name].
func buildAppStart(appName string) []byte {
	f := make([]byte, 8, 8+len(appName))
	f[0] = cmdAppStart
	return append(f, appName...)
}

// buildSetDeviceTime builds CMD_SET_DEVICE_TIME: [0x06][epoch u32 LE].
func buildSetDeviceTime(t time.Time) []byte {
	f := make([]byte, 5)
	f[0] = cmdSetDeviceTime
	binary.LittleEndian.PutUint32(f[1:], uint32(t.Unix()))
	return f
}

// buildSendTxtMsg builds CMD_SEND_TXT_MSG:
// [0x02][txt_type][attempt][sender_timestamp u32 LE][pubkey_prefix 6B][text].
func buildSendTxtMsg(toPrefix []byte, text string, attempt byte, t time.Time) []byte {
	f := make([]byte, 13, 13+len(text))
	f[0] = cmdSendTxtMsg
	f[1] = 0 // txt_type: plain
	f[2] = attempt
	binary.LittleEndian.PutUint32(f[3:], uint32(t.Unix()))
	copy(f[7:13], toPrefix)
	return append(f, text...)
}

// buildSyncNextMessage builds CMD_SYNC_NEXT_MESSAGE: [0x0A].
func buildSyncNextMessage() []byte { return []byte{cmdSyncNextMessage} }

// buildGetContacts builds CMD_GET_CONTACTS: [0x04].
func buildGetContacts() []byte { return []byte{cmdGetContacts} }

// protocolVersion is the companion-protocol version this client speaks.
// Declaring >= 3 makes the firmware send V3 message frames, which carry SNR.
const protocolVersion = 3

// buildDeviceQuery builds CMD_DEVICE_QUERY: [0x16][app protocol version].
func buildDeviceQuery() []byte { return []byte{cmdDeviceQuery, protocolVersion} }

// parseDeviceInfo parses RESP_CODE_DEVICE_INFO (0x0D) far enough to log:
// [0x0D][fw_ver], then for fw_ver >= 3: [max_contacts/2][max_channels]
// [ble_pin u32][fw_build 12B][model 40B][version 20B]...
func parseDeviceInfo(f []byte) (fwVer int, model string) {
	if len(f) < 2 || f[0] != respDeviceInfo {
		return 0, ""
	}
	fwVer = int(f[1])
	if len(f) >= 60 {
		model = strings.TrimRight(string(f[20:60]), "\x00")
	}
	return fwVer, model
}

// parseContact parses RESP_CODE_CONTACT (0x03):
//
//	[0x03][pubkey 32B][type][flags][out_path_len i8][out_path 64B]
//	[adv_name 32B nul-padded][last_advert u32][adv_lat i32][adv_lon i32][lastmod u32]
//
// Only the pubkey prefix and name are interesting to the bot.
func parseContact(f []byte) (prefix, name string, err error) {
	if len(f) < 132 || f[0] != respContact {
		return "", "", fmt.Errorf("meshcore: bad CONTACT frame (%d bytes)", len(f))
	}
	prefix = hex.EncodeToString(f[1:7])
	name = strings.TrimRight(string(f[100:132]), "\x00")
	return prefix, name, nil
}

// buildSendSelfAdvert builds CMD_SEND_SELF_ADVERT: [0x07][type] (1 = flood).
func buildSendSelfAdvert(flood bool) []byte {
	t := byte(0)
	if flood {
		t = 1
	}
	return []byte{cmdSendSelfAdvert, t}
}

// selfInfo is a parsed RESP_CODE_SELF_INFO frame.
type selfInfo struct {
	publicKey []byte // 32 bytes
	name      string
}

// parseSelfInfo parses RESP_CODE_SELF_INFO:
// [0x05][adv_type][tx_power][max_tx_power][pubkey 32B][lat i32][lon i32]
// [multi_acks][loc_policy][telemetry][manual_add][freq u32][bw u32][sf][cr][name...].
func parseSelfInfo(f []byte) (selfInfo, error) {
	if len(f) < 36 || f[0] != respSelfInfo {
		return selfInfo{}, fmt.Errorf("meshcore: bad SELF_INFO frame (%d bytes)", len(f))
	}
	si := selfInfo{publicKey: append([]byte(nil), f[4:36]...)}
	if len(f) > 58 {
		si.name = strings.TrimRight(string(f[58:]), "\x00")
	}
	return si, nil
}

// contactMsg is a parsed contact (DM) message frame.
type contactMsg struct {
	fromPrefix []byte // 6 bytes
	text       string
	snr        float64 // dB, 0 if not reported (v1 frames)
	hops       int     // route length; 0 = direct, -1 = unknown
	timestamp  time.Time
}

// parseContactMsg parses RESP_CODE_CONTACT_MSG_RECV (0x07):
//
//	[0x07][pubkey_prefix 6B][path_len][txt_type][timestamp u32][text...]
//
// and the V3 variant (0x10):
//
//	[0x10][snr i8*4][2 reserved][pubkey_prefix 6B][path_len][txt_type][timestamp u32][text...]
//
// txt_type 2 (signed plain) carries a 4-byte signature before the text.
func parseContactMsg(f []byte) (contactMsg, error) {
	var m contactMsg
	var off int
	switch f[0] {
	case respContactMsgRecv:
		off = 1
	case respContactMsgV3:
		if len(f) < 4 {
			return m, fmt.Errorf("meshcore: short v3 msg frame")
		}
		m.snr = float64(int8(f[1])) / 4.0
		off = 4
	default:
		return m, fmt.Errorf("meshcore: not a contact msg frame (code 0x%02x)", f[0])
	}
	if len(f) < off+12 {
		return m, fmt.Errorf("meshcore: short contact msg frame (%d bytes)", len(f))
	}
	m.fromPrefix = append([]byte(nil), f[off:off+6]...)
	// Receive-side path_len semantics: 0xFF = the packet was ROUTED along a
	// sender-prescribed path, which is consumed in transit — no hop count
	// reaches us (could be zero hops or several). A numeric value means it
	// was FLOODED and that many repeater hops were recorded on the way.
	if pathLen := f[off+6]; pathLen == 0xFF {
		m.hops = -1 // routed: hop count unknown
	} else {
		m.hops = int(pathLen)
	}
	txtType := f[off+7]
	m.timestamp = time.Unix(int64(binary.LittleEndian.Uint32(f[off+8:])), 0)
	textOff := off + 12
	if txtType == 2 { // signed: skip 4-byte signature
		textOff += 4
	}
	if len(f) > textOff {
		m.text = string(f[textOff:])
	}
	return m, nil
}

// prefixToBytes converts a 12-hex-char pubkey prefix to its 6 raw bytes.
func prefixToBytes(prefix string) ([]byte, error) {
	b, err := hex.DecodeString(prefix)
	if err != nil || len(b) != 6 {
		return nil, fmt.Errorf("meshcore: invalid pubkey prefix %q", prefix)
	}
	return b, nil
}
