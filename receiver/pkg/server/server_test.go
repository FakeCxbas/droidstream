package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net"
	"testing"
	"time"

	"github.com/FakeCxbas/droidstream/receiver/pkg/protocol"
)

func createDummyJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

func TestServerPipelineIntegration(t *testing.T) {
	// Start server on random local port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	jpegData := createDummyJPEG(64, 48)

	done := make(chan bool)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		hdr, err := protocol.ReadHeader(conn)
		if err != nil {
			t.Errorf("read header error: %v", err)
			return
		}

		if hdr.PayloadSize != uint32(len(jpegData)) {
			t.Errorf("got size %d, want %d", hdr.PayloadSize, len(jpegData))
		}

		buf := make([]byte, hdr.PayloadSize)
		_, err = conn.Read(buf)
		if err != nil {
			t.Errorf("read body error: %v", err)
			return
		}

		done <- true
	}()

	clientConn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer clientConn.Close()

	hdr := protocol.FrameHeader{
		Magic:       protocol.MagicBytes,
		Version:     protocol.Version,
		FrameType:   protocol.FrameTypeJPEG,
		Width:       64,
		Height:      48,
		PayloadSize: uint32(len(jpegData)),
		TimestampUs: uint64(time.Now().UnixMicro()),
	}

	hdrBuf := make([]byte, protocol.HeaderSize)
	hdr.Encode(hdrBuf)

	if _, err := clientConn.Write(hdrBuf); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}
	if _, err := clientConn.Write(jpegData); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frame transmission")
	}
}
