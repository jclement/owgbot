package meshcore

import (
	"bufio"
	"encoding/binary"
	"io"
)

// Serial framing for the companion protocol over USB:
//
//	app → radio: 0x3C ('<') [len u16 LE] [payload]
//	radio → app: 0x3E ('>') [len u16 LE] [payload]
const (
	frameStartOut = 0x3C
	frameStartIn  = 0x3E
	maxFrameLen   = 1024 // sanity bound; real frames are ≤ ~255 bytes
)

// writeFrame writes one app→radio frame.
func writeFrame(w io.Writer, payload []byte) error {
	hdr := []byte{frameStartOut, 0, 0}
	binary.LittleEndian.PutUint16(hdr[1:], uint16(len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// readFrame reads one radio→app frame, resynchronizing past any noise bytes
// before the 0x3E start marker (serial lines can carry boot logs / garbage).
func readFrame(r *bufio.Reader) ([]byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != frameStartIn {
			continue
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return nil, err
		}
		n := int(binary.LittleEndian.Uint16(lenBuf[:]))
		if n == 0 || n > maxFrameLen {
			// Implausible length: treat the start byte as noise and resync.
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
		return payload, nil
	}
}
