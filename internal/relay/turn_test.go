package relay

import (
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/pion/turn/v5"
)

// The credentials handed to peers must be accepted by the embedded server,
// and a wrong secret must not be.
func TestTURNAllocatesWithIssuedCredentials(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	_ = listener.Close()

	server, err := StartTURN(TURNConfig{
		Port: port, PublicIP: "127.0.0.1", Secret: "test-secret", MinPort: 49600, MaxPort: 49650,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	servers := server.ICEServers()
	if len(servers) != 2 || !strings.HasPrefix(servers[1].URLs[0], "turn:127.0.0.1:") {
		t.Fatalf("unexpected ICE servers %+v", servers)
	}
	username := servers[1].Username
	password, _ := servers[1].Credential.(string)

	allocate := func(password string) error {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer conn.Close()
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		client, err := turn.NewClient(&turn.ClientConfig{
			STUNServerAddr: address,
			TURNServerAddr: address,
			Conn:           conn,
			Username:       username,
			Password:       password,
			Realm:          "motionlink",
		})
		if err != nil {
			return err
		}
		defer client.Close()
		if err := client.Listen(); err != nil {
			return err
		}
		relayConn, err := client.Allocate()
		if err != nil {
			return err
		}
		return relayConn.Close()
	}

	if err := allocate(password); err != nil {
		t.Fatalf("allocation with issued credentials failed: %v", err)
	}
	if err := allocate("wrong-password"); err == nil {
		t.Fatalf("allocation with a wrong password succeeded")
	}
}
