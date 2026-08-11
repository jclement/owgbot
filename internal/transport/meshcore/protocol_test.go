package meshcore

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte{0x01, 0xAA, 0xBB}
	if err := writeFrame(&buf, payload); err != nil {
		t.Fatal(err)
	}
	// Outbound start byte is 0x3C; flip to 0x3E to simulate the radio side.
	raw := buf.Bytes()
	if raw[0] != 0x3C {
		t.Fatalf("outbound start byte 0x%02x", raw[0])
	}
	raw[0] = 0x3E
	got, err := readFrame(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %x want %x", got, payload)
	}
}

func TestReadFrameResyncsPastNoise(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("boot garbage\n")
	buf.WriteByte(0x3E)
	buf.Write([]byte{0x02, 0x00}) // len 2
	buf.Write([]byte{0x0A, 0x00})
	got, err := readFrame(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{0x0A, 0x00}) {
		t.Fatalf("got %x", got)
	}
}

func TestBuildSendTxtMsg(t *testing.T) {
	prefix := []byte{1, 2, 3, 4, 5, 6}
	now := time.Unix(1234567890, 0)
	f := buildSendTxtMsg(prefix, "hi", 0, now)
	if f[0] != cmdSendTxtMsg || f[1] != 0 || f[2] != 0 {
		t.Fatalf("header wrong: %x", f[:3])
	}
	if binary.LittleEndian.Uint32(f[3:7]) != 1234567890 {
		t.Fatal("timestamp wrong")
	}
	if !bytes.Equal(f[7:13], prefix) {
		t.Fatal("prefix wrong")
	}
	if string(f[13:]) != "hi" {
		t.Fatalf("text wrong: %q", f[13:])
	}
}

func TestParseContactMsgV1(t *testing.T) {
	f := []byte{respContactMsgRecv}
	f = append(f, 1, 2, 3, 4, 5, 6) // prefix
	f = append(f, 0)                // path_len
	f = append(f, 0)                // txt_type plain
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, 1700000000)
	f = append(f, ts...)
	f = append(f, []byte("/help")...)

	m, err := parseContactMsg(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.text != "/help" {
		t.Fatalf("text %q", m.text)
	}
	if m.timestamp.Unix() != 1700000000 {
		t.Fatal("timestamp wrong")
	}
	if !bytes.Equal(m.fromPrefix, []byte{1, 2, 3, 4, 5, 6}) {
		t.Fatal("prefix wrong")
	}
}

func TestParseContactMsgV3WithSNR(t *testing.T) {
	f := []byte{respContactMsgV3, 0xF8, 0, 0} // SNR -8/4 = -2dB
	f = append(f, 9, 8, 7, 6, 5, 4)           // prefix
	f = append(f, 2)                          // path_len: flooded, 2 hops
	f = append(f, 0)                          // txt_type
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, 1700000001)
	f = append(f, ts...)
	f = append(f, []byte("/w yyc")...)

	m, err := parseContactMsg(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.snr != -2.0 {
		t.Fatalf("snr %v", m.snr)
	}
	if m.hops != 2 {
		t.Fatalf("hops %d", m.hops)
	}
	if m.text != "/w yyc" {
		t.Fatalf("text %q", m.text)
	}
}

// Receive-side path_len 0xFF means the message arrived DIRECT.
func TestParseContactMsgDirectPath(t *testing.T) {
	f := []byte{respContactMsgV3, 0x10, 0, 0} // SNR +4dB
	f = append(f, 1, 2, 3, 4, 5, 6)
	f = append(f, 0xFF) // direct
	f = append(f, 0)
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, 1700000002)
	f = append(f, ts...)
	f = append(f, []byte("/ping")...)

	m, err := parseContactMsg(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.hops != 0 {
		t.Fatalf("0xFF path must mean direct (0 hops), got %d", m.hops)
	}
}

func TestParseSelfInfo(t *testing.T) {
	f := make([]byte, 58)
	f[0] = respSelfInfo
	for i := 0; i < 32; i++ {
		f[4+i] = byte(i)
	}
	f = append(f, []byte("OWG Bot")...)
	si, err := parseSelfInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if si.name != "OWG Bot" {
		t.Fatalf("name %q", si.name)
	}
	if si.publicKey[0] != 0 || si.publicKey[31] != 31 {
		t.Fatal("pubkey wrong")
	}
}
