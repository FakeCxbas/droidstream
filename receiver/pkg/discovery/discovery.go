package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"
)

const (
	DefaultBroadcastPort = 4749
	ServiceName          = "droidstream"
)

type BeaconMessage struct {
	Service string   `json:"service"`
	Port    int      `json:"port"`
	IPs     []string `json:"ips"`
}

type QueryMessage struct {
	Query string `json:"query"`
}

// GetLocalIPs returns non-loopback IPv4 addresses of active network interfaces.
func GetLocalIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}
	return ips
}

// StartBroadcaster broadcasts UDP beacons on the local subnet to allow Android devices
// to automatically discover the PC's IP and port without manual typing.
func StartBroadcaster(ctx context.Context, tcpPort int, broadcastPort int) error {
	listenAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("0.0.0.0:%d", broadcastPort))
	if err != nil {
		return fmt.Errorf("failed to resolve udp listen address: %w", err)
	}

	conn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to start udp discovery listener on port %d: %w", broadcastPort, err)
	}

	broadcastAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", broadcastPort))
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to resolve udp broadcast target: %w", err)
	}

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	// Query listener goroutine: responds immediately to Android search queries
	go func() {
		buf := make([]byte, 1024)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, remoteAddr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}

			var q QueryMessage
			if err := json.Unmarshal(buf[:n], &q); err == nil && q.Query == ServiceName {
				msg := BeaconMessage{
					Service: ServiceName,
					Port:    tcpPort,
					IPs:     GetLocalIPs(),
				}
				payload, _ := json.Marshal(msg)
				_, _ = conn.WriteTo(payload, remoteAddr)
			}
		}
	}()

	// Periodic beacon goroutine: broadcasts every 2 seconds
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ips := GetLocalIPs()
				if len(ips) == 0 {
					continue
				}
				msg := BeaconMessage{
					Service: ServiceName,
					Port:    tcpPort,
					IPs:     ips,
				}
				payload, err := json.Marshal(msg)
				if err != nil {
					continue
				}

				if _, err := conn.WriteTo(payload, broadcastAddr); err != nil {
					// Ignore network unreachable errors if interfaces change
					continue
				}
			}
		}
	}()

	log.Printf("[discovery] servicio de auto-descubrimiento Wi-Fi activo en UDP:%d", broadcastPort)
	return nil
}
