package discovery

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestGetLocalIPs(t *testing.T) {
	ips := GetLocalIPs()
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			t.Errorf("invalid IP returned: %s", ip)
		}
		if parsed.IsLoopback() {
			t.Errorf("GetLocalIPs should not return loopback: %s", ip)
		}
	}
}

func TestDiscoveryBroadcastAndQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testPort := 47491
	tcpPort := 4747

	err := StartBroadcaster(ctx, tcpPort, testPort)
	if err != nil {
		t.Fatalf("StartBroadcaster failed: %v", err)
	}

	// Create client to query the broadcaster
	clientConn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: testPort,
	})
	if err != nil {
		t.Fatalf("failed to dial udp: %v", err)
	}
	defer clientConn.Close()

	_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))

	query := QueryMessage{Query: ServiceName}
	queryBytes, _ := json.Marshal(query)
	if _, err := clientConn.Write(queryBytes); err != nil {
		t.Fatalf("failed to write query: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("failed to read query response: %v", err)
	}

	var beacon BeaconMessage
	if err := json.Unmarshal(buf[:n], &beacon); err != nil {
		t.Fatalf("failed to unmarshal beacon: %v", err)
	}

	if beacon.Service != ServiceName {
		t.Errorf("expected service %s, got %s", ServiceName, beacon.Service)
	}
	if beacon.Port != tcpPort {
		t.Errorf("expected port %d, got %d", tcpPort, beacon.Port)
	}
}
