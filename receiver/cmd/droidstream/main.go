package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/FakeCxbas/droidstream/receiver/pkg/server"
	"github.com/FakeCxbas/droidstream/receiver/pkg/v4l2"
)

func main() {
	devicePath := flag.String("dev", "/dev/video2", "Path to v4l2loopback video device")
	port := flag.Int("port", 4747, "TCP port to listen on")
	width := flag.Int("width", 1280, "Frame width in pixels")
	height := flag.Int("height", 720, "Frame height in pixels")
	formatStr := flag.String("format", "yu12", "Video format: yu12, yuyv, mjpeg")
	enableADB := flag.Bool("adb", false, "Automatically configure adb forward tunnel for USB mode")
	webPort := flag.Int("web", 4748, "HTTP port for web monitor preview (0 to disable)")

	flag.Parse()

	var fmtCode v4l2.FourCC
	switch strings.ToLower(*formatStr) {
	case "yu12", "yuv420":
		fmtCode = v4l2.PixelFormatYUV420
	case "yuyv":
		fmtCode = v4l2.PixelFormatYUYV
	case "mjpeg", "mjpg":
		fmtCode = v4l2.PixelFormatMJPEG
	default:
		log.Fatalf("unsupported format '%s', allowed: yu12, yuyv, mjpeg", *formatStr)
	}

	if *enableADB {
		log.Printf("[adb] configuring reverse port tunnel: adb reverse tcp:%d tcp:%d ...\n", *port, *port)
		// Clean up previous forward if present
		_ = exec.Command("adb", "forward", "--remove", fmt.Sprintf("tcp:%d", *port)).Run()
		cmd := exec.Command("adb", "reverse", fmt.Sprintf("tcp:%d", *port), fmt.Sprintf("tcp:%d", *port))
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[adb] warning: failed to set adb reverse: %v (%s)", err, strings.TrimSpace(string(out)))
		} else {
			log.Println("[adb] reverse tunnel active (phone 127.0.0.1 -> PC)")
		}
	}

	cfg := server.Config{
		ListenAddr: fmt.Sprintf("0.0.0.0:%d", *port),
		DevicePath: *devicePath,
		Width:      uint32(*width),
		Height:     uint32(*height),
		Format:     fmtCode,
		WebPort:    *webPort,
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("failed to initialize server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("[server] shutdown signal received")
		cancel()
	}()

	if err := srv.Start(ctx); err != nil {
		log.Fatalf("server terminated with error: %v", err)
	}
}
