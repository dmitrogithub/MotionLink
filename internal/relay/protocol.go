// Package relay implements the MotionLink rendezvous server and the host-side
// client for it.
//
// A host behind NAT cannot accept incoming HTTP requests, so it long-polls a
// public relay instead. A client posts its signed offer to the same "room"
// (a hash of the access key) and the relay hands it to the host and the
// signed answer back. The relay never sees the key and cannot forge either
// message. It can also run a TURN server that tunnels the media when the two
// NATs do not allow a direct path.
package relay

import (
	"encoding/json"
	"regexp"

	"github.com/pion/webrtc/v4"
)

// PollResponse delivers one client offer to the host.
type PollResponse struct {
	ID         string             `json:"id"`
	From       string             `json:"from"`
	Offer      json.RawMessage    `json:"offer"`
	ICEServers []webrtc.ICEServer `json:"iceServers"`
}

// Reply is the host's response to one offer; the relay returns Body to the
// waiting client with Status.
type Reply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

var roomPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidRoom reports whether room looks like auth.RoomID output.
func ValidRoom(room string) bool {
	return roomPattern.MatchString(room)
}
