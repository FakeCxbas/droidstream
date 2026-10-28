package protocol

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeHeader(t *testing.T) {
	orig := FrameHeader{
		Magic:       MagicBytes,
		Version:     1,
		FrameType:   FrameTypeJPEG,
		Width:       1920,
		Height:      1080,
		PayloadSize: 65536,
		TimestampUs: 1729000000000,
	}

	buf := make([]byte, HeaderSize)
	orig.Encode(buf)

	reader := bytes.NewReader(buf)
	decoded, err := ReadHeader(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *decoded != orig {
		t.Fatalf("decoded header mismatch: got %+v, want %+v", decoded, orig)
	}
}

func TestInvalidMagic(t *testing.T) {
	buf := make([]byte, HeaderSize)
	copy(buf[0:4], []byte("TEST"))
	reader := bytes.NewReader(buf)
	_, err := ReadHeader(reader)
	if err != ErrInvalidMagic {
		t.Fatalf("expected ErrInvalidMagic, got %v", err)
	}
}
