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

	"strconv"

	"github.com/FakeCxbas/droidstream/receiver/pkg/discovery"
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
	FramesReceived uint64   `json:"frames"`
	BytesReceived  uint64   `json:"bytes"`
	CurrentFPS     float64  `json:"fps"`
	CurrentKbps    float64  `json:"kbps"`
	DevicePath     string   `json:"device"`
	Connected      bool     `json:"connected"`
	ClientAddr     string   `json:"client_addr"`
	LocalIPs       []string `json:"local_ips"`
	Width          uint32   `json:"width"`
	Height         uint32   `json:"height"`
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

	clientMu        sync.RWMutex
	clientConnected bool
	clientAddr      string
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

	// Start UDP discovery beacon for Wi-Fi auto-detection
	_, portStr, _ := net.SplitHostPort(s.config.ListenAddr)
	portNum, _ := strconv.Atoi(portStr)
	if portNum == 0 {
		portNum = 4747
	}
	_ = discovery.StartBroadcaster(ctx, portNum, discovery.DefaultBroadcastPort)

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

	s.clientMu.Lock()
	s.clientConnected = true
	s.clientAddr = conn.RemoteAddr().String()
	s.clientMu.Unlock()

	defer func() {
		s.clientMu.Lock()
		s.clientConnected = false
		s.clientAddr = ""
		s.clientMu.Unlock()
	}()

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
		ips := discovery.GetLocalIPs()
		primaryIP := "127.0.0.1"
		if len(ips) > 0 {
			primaryIP = ips[0]
		}
		_, portStr, _ := net.SplitHostPort(s.config.ListenAddr)

		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="es">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>DroidStream - Panel Interactivo</title>
    <style>
        :root {
            --bg-base: #0b0d14;
            --bg-card: #151824;
            --bg-item: #1f2334;
            --accent-green: #00e676;
            --accent-blue: #3d8bfd;
            --text-main: #f0f3f6;
            --text-muted: #8b949e;
            --border: #2b3145;
        }
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { background: var(--bg-base); color: var(--text-main); font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; padding: 20px; display: flex; justify-content: center; }
        .app-window { max-width: 1100px; width: 100%%; display: flex; flex-direction: column; gap: 20px; }
        header { display: flex; justify-content: space-between; align-items: center; padding-bottom: 12px; border-bottom: 1px solid var(--border); }
        .logo-area { display: flex; align-items: center; gap: 10px; }
        .logo-title { font-size: 20px; font-weight: 700; letter-spacing: -0.5px; }
        .badge-live { background: #2e7d32; color: #fff; font-size: 11px; font-weight: 700; padding: 3px 8px; border-radius: 6px; text-transform: uppercase; }
        .badge-waiting { background: #374151; color: #9ca3af; font-size: 11px; font-weight: 700; padding: 3px 8px; border-radius: 6px; text-transform: uppercase; }
        
        .main-layout { display: grid; grid-template-columns: 2fr 1.2fr; gap: 20px; }
        @media (max-width: 860px) { .main-layout { grid-template-columns: 1fr; } }

        .card { background: var(--bg-card); border-radius: 12px; border: 1px solid var(--border); padding: 16px; }
        .video-container { position: relative; width: 100%%; aspect-ratio: 16 / 9; background: #000; border-radius: 8px; overflow: hidden; display: flex; align-items: center; justify-content: center; }
        .video-feed { width: 100%%; height: 100%%; object-fit: contain; }
        .video-overlay { position: absolute; top: 12px; left: 12px; right: 12px; display: flex; justify-content: space-between; pointer-events: none; }
        .overlay-pill { background: rgba(0,0,0,0.65); backdrop-filter: blur(4px); padding: 4px 10px; border-radius: 6px; font-size: 12px; font-weight: 600; }
        .btn-fullscreen { pointer-events: auto; background: rgba(0,0,0,0.65); border: 1px solid rgba(255,255,255,0.2); color: #fff; padding: 4px 10px; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: 600; }

        .tabs { display: flex; gap: 8px; margin-bottom: 16px; border-bottom: 1px solid var(--border); padding-bottom: 8px; }
        .tab-btn { background: none; border: none; color: var(--text-muted); font-size: 14px; font-weight: 600; padding: 6px 12px; cursor: pointer; border-radius: 6px; transition: all 0.2s; }
        .tab-btn.active { color: var(--text-main); background: var(--bg-item); }
        .tab-content { display: none; }
        .tab-content.active { display: block; }

        .ip-box { background: var(--bg-item); border: 1px solid var(--border); border-radius: 8px; padding: 12px; margin-bottom: 12px; display: flex; justify-content: space-between; align-items: center; }
        .ip-label { font-size: 12px; color: var(--text-muted); margin-bottom: 4px; }
        .ip-value { font-size: 18px; font-weight: 700; color: var(--accent-blue); font-family: monospace; }
        .btn-copy { background: #2b3145; border: none; color: #fff; padding: 6px 12px; border-radius: 6px; font-size: 12px; cursor: pointer; font-weight: 600; }
        .btn-copy:hover { background: #3b425b; }

        .stats-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-top: 14px; }
        .stat-card { background: var(--bg-item); border-radius: 8px; padding: 12px; text-align: center; }
        .stat-number { font-size: 22px; font-weight: 700; color: var(--accent-green); }
        .stat-title { font-size: 11px; color: var(--text-muted); margin-top: 2px; }

        .device-info { margin-top: 14px; font-size: 12px; color: var(--text-muted); background: var(--bg-item); padding: 10px; border-radius: 8px; line-height: 1.5; }
    </style>
</head>
<body>
    <div class="app-window">
        <header>
            <div class="logo-area">
                <span class="logo-title">DroidStream</span>
                <span id="badge-status" class="badge-waiting">Esperando celular</span>
            </div>
            <div style="font-size: 12px; color: var(--text-muted);">
                Salida: <span style="color: var(--accent-blue); font-weight: 600;">%s (%dx%d 16:9)</span>
            </div>
        </header>

        <div class="main-layout">
            <div class="card" style="display: flex; flex-direction: column;">
                <div class="video-container" id="videoBox">
                    <img id="streamImg" class="video-feed" src="/stream" alt="Esperando conexion de video...">
                    <div class="video-overlay">
                        <div class="overlay-pill" id="overlayFps">0.0 FPS</div>
                        <button class="btn-fullscreen" onclick="toggleFullScreen()">Pantalla Completa</button>
                    </div>
                </div>
                <div style="display: flex; justify-content: space-between; align-items: center; margin-top: 12px; font-size: 12px; color: var(--text-muted);">
                    <span id="txtClient">Sin dispositivo conectado</span>
                    <span id="txtResolution">Proporcion: 16:9 Panoramica</span>
                </div>
            </div>

            <div class="card">
                <div class="tabs">
                    <button class="tab-btn active" onclick="switchTab('wifi', this)">Conexion Wi-Fi</button>
                    <button class="tab-btn" onclick="switchTab('usb', this)">Conexion USB</button>
                </div>

                <div id="tab-wifi" class="tab-content active">
                    <div class="ip-box">
                        <div>
                            <div class="ip-label">Direccion IP de la PC (Wi-Fi local)</div>
                            <div class="ip-value" id="displayIP">%s</div>
                        </div>
                        <button class="btn-copy" id="btnCopyIP" onclick="copyHost()">Copiar IP</button>
                    </div>
                    <div style="font-size: 12px; color: var(--text-muted); line-height: 1.5; margin-bottom: 12px;">
                        • Puerto de transmision: <strong style="color: var(--text-main);">%s</strong><br>
                        • Auto-descubrimiento activo en UDP: <strong>4749</strong>.<br>
                        • En la app Android, pulsa <strong>Detectar PC</strong> o introduce esta IP en el campo Host.<br>
                        • Si hay cortafuegos activo en Linux (UFW), autoriza los puertos con: <code style="color: var(--accent-green); background: #000; padding: 2px 6px; border-radius: 4px;">make setup-firewall</code>
                    </div>
                </div>

                <div id="tab-usb" class="tab-content">
                    <div class="ip-box">
                        <div>
                            <div class="ip-label">Host local (Tunel ADB reverse)</div>
                            <div class="ip-value">127.0.0.1</div>
                        </div>
                    </div>
                    <div style="font-size: 12px; color: var(--text-muted); line-height: 1.5; margin-bottom: 12px;">
                        • Conecta el movil por USB con depuracion activa.<br>
                        • El receptor configura automaticamente el tunel inverso hacia la PC sin latencia.
                    </div>
                </div>

                <div class="stats-grid">
                    <div class="stat-card">
                        <div class="stat-number" id="valFps">0.0</div>
                        <div class="stat-title">FPS en Tiempo Real</div>
                    </div>
                    <div class="stat-card">
                        <div class="stat-number" id="valBitrate">0.0</div>
                        <div class="stat-title">Bitrate (kbps)</div>
                    </div>
                    <div class="stat-card">
                        <div class="stat-number" id="valFrames">0</div>
                        <div class="stat-title">Tramas Procesadas</div>
                    </div>
                    <div class="stat-card">
                        <div class="stat-number" id="valData">0.0 MB</div>
                        <div class="stat-title">Datos Transferidos</div>
                    </div>
                </div>

                <div class="device-info">
                    <strong>Integracion Linux:</strong> Tramas reescaladas a 1280x720 YU12 e inyectadas directamente en V4L2loopback. Compatible de forma nativa con Discord, OBS Studio, Google Meet y Zoom.
                </div>
            </div>
        </div>
    </div>

    <script>
        function switchTab(name, btn) {
            document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
            document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
            btn.classList.add('active');
            document.getElementById('tab-' + name).classList.add('active');
        }

        function copyHost() {
            const ip = document.getElementById('displayIP').innerText;
            navigator.clipboard.writeText(ip).then(() => {
                const btn = document.getElementById('btnCopyIP');
                btn.innerText = 'Copiado!';
                setTimeout(() => { btn.innerText = 'Copiar IP'; }, 1500);
            });
        }

        function toggleFullScreen() {
            const elem = document.getElementById('videoBox');
            if (!document.fullscreenElement) {
                elem.requestFullscreen().catch(() => {});
            } else {
                document.exitFullscreen().catch(() => {});
            }
        }

        setInterval(() => {
            fetch('/stats').then(r => r.json()).then(data => {
                document.getElementById('valFps').innerText = data.fps.toFixed(1);
                document.getElementById('overlayFps').innerText = data.fps.toFixed(1) + ' FPS';
                document.getElementById('valBitrate').innerText = data.kbps.toFixed(1);
                document.getElementById('valFrames').innerText = data.frames.toLocaleString();
                
                const mb = (data.bytes / (1024 * 1024)).toFixed(1);
                document.getElementById('valData').innerText = mb + ' MB';

                const badge = document.getElementById('badge-status');
                const txtClient = document.getElementById('txtClient');

                if (data.connected) {
                    badge.className = 'badge-live';
                    badge.innerText = 'Transmitiendo en vivo';
                    txtClient.innerText = 'Dispositivo conectado: ' + data.client_addr;
                } else {
                    badge.className = 'badge-waiting';
                    badge.innerText = 'Esperando celular';
                    txtClient.innerText = 'Sin dispositivo conectado';
                }

                if (data.local_ips && data.local_ips.length > 0) {
                    document.getElementById('displayIP').innerText = data.local_ips[0];
                }
            }).catch(() => {});
        }, 1000);
    </script>
</body>
</html>`, s.config.DevicePath, s.config.Width, s.config.Height, primaryIP, portStr)
	})

	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fps, _ := s.lastFPS.Load().(float64)
		kbps, _ := s.lastKbps.Load().(float64)

		s.clientMu.RLock()
		connected := s.clientConnected
		clientAddr := s.clientAddr
		s.clientMu.RUnlock()

		stats := Stats{
			FramesReceived: atomic.LoadUint64(&s.framesCount),
			BytesReceived:  atomic.LoadUint64(&s.bytesCount),
			CurrentFPS:     fps,
			CurrentKbps:    kbps,
			DevicePath:     s.config.DevicePath,
			Connected:      connected,
			ClientAddr:     clientAddr,
			LocalIPs:       discovery.GetLocalIPs(),
			Width:          s.config.Width,
			Height:         s.config.Height,
		}
		_ = json.NewEncoder(w).Encode(stats)
	})

	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := map[string]interface{}{
			"service":     "droidstream",
			"version":     "1.0.0",
			"device_path": s.config.DevicePath,
			"width":       s.config.Width,
			"height":      s.config.Height,
			"listen_addr": s.config.ListenAddr,
			"web_port":    s.config.WebPort,
			"local_ips":   discovery.GetLocalIPs(),
		}
		_ = json.NewEncoder(w).Encode(info)
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
