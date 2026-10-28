package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
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
	WebPort    int
}

type Stats struct {
	FramesReceived uint64  `json:"frames"`
	BytesReceived  uint64  `json:"bytes"`
	CurrentFPS     float64 `json:"fps"`
	CurrentKbps    float64 `json:"kbps"`
	DevicePath     string  `json:"device"`
}

type broadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan []byte]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{
		subscribers: make(map[chan []byte]struct{}),
	}
}

func (b *broadcaster) subscribe() chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []byte, 2)
	b.subscribers[ch] = struct{}{}
	return ch
}

func (b *broadcaster) unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
	close(ch)
}

func (b *broadcaster) broadcast(data []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- data:
		default:
			// Drop frame if subscriber channel is full to prevent backpressure
		}
	}
}

type Server struct {
	config Config
	device *v4l2.Device

	framesCount uint64
	bytesCount  uint64
	lastFPS     atomic.Value
	lastKbps    atomic.Value
	broadcaster *broadcaster
}

func New(cfg Config) (*Server, error) {
	dev, err := v4l2.Open(cfg.DevicePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open device: %w", err)
	}

	if err := dev.SetFormat(cfg.Width, cfg.Height, cfg.Format); err != nil {
		log.Printf("[v4l2] note: setFormat returned %v (using existing driver config)", err)
	}

	s := &Server{
		config:      cfg,
		device:      dev,
		broadcaster: newBroadcaster(),
	}
	s.lastFPS.Store(float64(0))
	s.lastKbps.Store(float64(0))
	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	defer s.device.Close()

	if s.config.WebPort > 0 {
		go s.startWebServer(ctx)
	}

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

		// Broadcast to web preview clients
		if header.FrameType == protocol.FrameTypeJPEG {
			frameCopy := make([]byte, len(frameData))
			copy(frameCopy, frameData)
			s.broadcaster.broadcast(frameCopy)
		}

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

func (s *Server) startWebServer(ctx context.Context) {
	addr := fmt.Sprintf("0.0.0.0:%d", s.config.WebPort)
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="es">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>DroidStream - Monitor en Vivo</title>
    <style>
        body { background: #0f111a; color: #e0e0e0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 0; padding: 24px; display: flex; flex-direction: column; align-items: center; }
        .container { max-width: 800px; width: 100%%; }
        h1 { font-size: 20px; font-weight: 600; margin-bottom: 16px; color: #fff; }
        .card { background: #1a1c26; border-radius: 12px; padding: 16px; border: 1px solid #2d3142; }
        .stream-view { width: 100%%; max-height: 540px; border-radius: 8px; background: #000; object-fit: contain; }
        .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 12px; margin-top: 16px; }
        .stat-item { background: #232738; padding: 12px; border-radius: 8px; text-align: center; }
        .stat-val { font-size: 20px; font-weight: 700; color: #00e676; }
        .stat-lbl { font-size: 12px; color: #90a4ae; margin-top: 4px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>DroidStream - Monitor de Camara Virtual</h1>
        <div class="card">
            <img class="stream-view" src="/stream" alt="Esperando conexion de video..." onerror="this.alt='Esperando tramas de video...'">
            <div class="stats-grid">
                <div class="stat-item"><div class="stat-val" id="stat-fps">0.0</div><div class="stat-lbl">FPS</div></div>
                <div class="stat-item"><div class="stat-val" id="stat-kbps">0.0</div><div class="stat-lbl">kbps</div></div>
                <div class="stat-item"><div class="stat-val" id="stat-frames">0</div><div class="stat-lbl">Tramas</div></div>
                <div class="stat-item"><div class="stat-val" style="color: #64b5f6;">%s</div><div class="stat-lbl">Dispositivo Kernel</div></div>
            </div>
        </div>
    </div>
    <script>
        setInterval(() => {
            fetch('/stats').then(r => r.json()).then(data => {
                document.getElementById('stat-fps').innerText = data.fps.toFixed(1);
                document.getElementById('stat-kbps').innerText = data.kbps.toFixed(1);
                document.getElementById('stat-frames').innerText = data.frames;
            }).catch(() => {});
        }, 1000);
    </script>
</body>
</html>`, s.config.DevicePath)
	})

	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fps, _ := s.lastFPS.Load().(float64)
		kbps, _ := s.lastKbps.Load().(float64)
		stats := Stats{
			FramesReceived: atomic.LoadUint64(&s.framesCount),
			BytesReceived:  atomic.LoadUint64(&s.bytesCount),
			CurrentFPS:     fps,
			CurrentKbps:    kbps,
			DevicePath:     s.config.DevicePath,
		}
		_ = json.NewEncoder(w).Encode(stats)
	})

	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.Header().Set("Cache-Control", "no-cache, private, max-age=0, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Connection", "close")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ch := s.broadcaster.subscribe()
		defer s.broadcaster.unsubscribe(ch)

		notify := r.Context().Done()

		for {
			select {
			case <-notify:
				return
			case <-ctx.Done():
				return
			case frame, ok := <-ch:
				if !ok {
					return
				}
				header := fmt.Sprintf("--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
				if _, err := w.Write([]byte(header)); err != nil {
					return
				}
				if _, err := w.Write(frame); err != nil {
					return
				}
				if _, err := w.Write([]byte("\r\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	log.Printf("[web] monitor disponible en http://localhost:%d", s.config.WebPort)

	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[web] error en servidor http: %v", err)
	}
}
