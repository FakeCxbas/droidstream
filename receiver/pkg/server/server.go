package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/FakeCxbas/droidstream/receiver/pkg/protocol"
	"github.com/FakeCxbas/droidstream/receiver/pkg/v4l2"
)

type Config struct {
	ListenAddr string
	DevicePath string
	Width      uint32
	Height     uint32
	Format     v4l2.FourCC
}

type Stats struct {
	FramesReceived uint64
	BytesReceived  uint64
	CurrentFPS     float64
	CurrentKbps    float64
}

type Server struct {
	config Config
	device *v4l2.Device

	framesCount uint64
	bytesCount  uint64
	lastFPS     atomic.Value
	lastKbps    atomic.Value
}

func New(cfg Config) (*Server, error) {
	dev, err := v4l2.Open(cfg.DevicePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open device: %w", err)
	}

	if err := dev.SetFormat(cfg.Width, cfg.Height, cfg.Format); err != nil {
		// Log warning but don't fail, device might have fixed caps or already configured
		log.Printf("[v4l2] note: setFormat returned %v (using existing driver config)", err)
	}

	s := &Server{
		config: cfg,
		device: dev,
	}
	s.lastFPS.Store(float64(0))
	s.lastKbps.Store(float64(0))
	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	defer s.device.Close()

	listener, err := net.Listen("tcp", s.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.config.ListenAddr, err)
	}
	defer listener.Close()

	log.Printf("[server] listening on %s -> writing to %s (%dx%d)",
		s.config.ListenAddr, s.config.DevicePath, s.config.Width, s.config.Height)

	go s.metricsLoop(ctx)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("[server] accept error: %v", err)
				continue
			}
		}

		log.Printf("[server] client connected from %s", conn.RemoteAddr().String())
		s.handleClient(ctx, conn)
		log.Printf("[server] client disconnected (%s)", conn.RemoteAddr().String())
	}
}

func (s *Server) handleClient(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	payloadBuf := make([]byte, 2*1024*1024)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		header, err := protocol.ReadHeader(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[protocol] read header error: %v", err)
			}
			return
		}

		if uint32(len(payloadBuf)) < header.PayloadSize {
			payloadBuf = make([]byte, header.PayloadSize)
		}

		frameData := payloadBuf[:header.PayloadSize]
		if _, err := io.ReadFull(conn, frameData); err != nil {
			log.Printf("[protocol] read payload error: %v", err)
			return
		}

		atomic.AddUint64(&s.framesCount, 1)
		atomic.AddUint64(&s.bytesCount, uint64(protocol.HeaderSize+header.PayloadSize))

		// Route frame to v4l2 device
		switch header.FrameType {
		case protocol.FrameTypeJPEG:
			if s.config.Format == v4l2.PixelFormatYUV420 {
				if err := s.device.WriteJPEGAsYU12(frameData); err != nil {
					log.Printf("[v4l2] write jpeg->yu12 error: %v", err)
				}
			} else {
				if _, err := s.device.WriteFrame(frameData); err != nil {
					log.Printf("[v4l2] write direct error: %v", err)
				}
			}
		case protocol.FrameTypeYU12, protocol.FrameTypeYUYV:
			if _, err := s.device.WriteFrame(frameData); err != nil {
				log.Printf("[v4l2] write raw error: %v", err)
			}
		default:
			log.Printf("[protocol] unsupported frame type: 0x%02x", header.FrameType)
		}
	}
}

func (s *Server) metricsLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var prevFrames, prevBytes uint64
	prevTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			elapsed := now.Sub(prevTime).Seconds()
			curFrames := atomic.LoadUint64(&s.framesCount)
			curBytes := atomic.LoadUint64(&s.bytesCount)

			fps := float64(curFrames-prevFrames) / elapsed
			kbps := (float64(curBytes-prevBytes) * 8 / 1024) / elapsed

			s.lastFPS.Store(fps)
			s.lastKbps.Store(kbps)

			if fps > 0.1 {
				log.Printf("[stats] %.1f FPS | %.1f kbps | frames: %d", fps, kbps, curFrames)
			}

			prevFrames = curFrames
			prevBytes = curBytes
			prevTime = now
		}
	}
}
