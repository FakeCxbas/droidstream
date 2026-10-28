package protocol

import (
	"encoding/binary"
	"errors"
	"io"
)

var (
	MagicBytes = [4]byte{'D', 'S', 'T', 'R'}
	Version    = uint8(1)

	ErrInvalidMagic   = errors.New("invalid protocol magic bytes")
	ErrUnsupportedVer = errors.New("unsupported protocol version")
	ErrPayloadTooBig  = errors.New("payload length exceeds maximum allowed limit")
)

const (
	FrameTypeJPEG    uint8 = 0x01
	FrameTypeYUYV    uint8 = 0x02
	FrameTypeYU12    uint8 = 0x03
	FrameTypeH264    uint8 = 0x04

	HeaderSize          = 22
	MaxPayloadSize uint32 = 16 * 1024 * 1024 // 16 MB max frame size
)

type FrameHeader struct {
	Magic       [4]byte
	Version     uint8
	FrameType   uint8
	Width       uint16
	Height      uint16
	PayloadSize uint32
	TimestampUs uint64
}

func (h *FrameHeader) Encode(buf []byte) {
	copy(buf[0:4], h.Magic[:])
	buf[4] = h.Version
	buf[5] = h.FrameType
	binary.BigEndian.PutUint16(buf[6:8], h.Width)
	binary.BigEndian.PutUint16(buf[8:10], h.Height)
	binary.BigEndian.PutUint32(buf[10:14], h.PayloadSize)
	binary.BigEndian.PutUint64(buf[14:22], h.TimestampUs)
}

func ReadHeader(r io.Reader) (*FrameHeader, error) {
	buf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	var h FrameHeader
	copy(h.Magic[:], buf[0:4])
	if h.Magic != MagicBytes {
		return nil, ErrInvalidMagic
	}

	h.Version = buf[4]
	if h.Version != Version {
		return nil, ErrUnsupportedVer
	}

	h.FrameType = buf[5]
	h.Width = binary.BigEndian.Uint16(buf[6:8])
	h.Height = binary.BigEndian.Uint16(buf[8:10])
	h.PayloadSize = binary.BigEndian.Uint32(buf[10:14])
	h.TimestampUs = binary.BigEndian.Uint64(buf[14:22])

	if h.PayloadSize > MaxPayloadSize {
		return nil, ErrPayloadTooBig
	}

	return &h, nil
}
