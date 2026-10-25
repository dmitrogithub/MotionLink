package rtc

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// udpReadBuffer absorbs keyframe bursts at high bitrates; the OS default
// (64 KiB on Windows) drops packets of a 25 Mbit/s IDR frame.
const udpReadBuffer = 4 << 20

// RTPRelay receives H.264 RTP packets emitted by FFmpeg and forwards them to
// a Pion TrackLocalStaticRTP. A relay is owned by one client session.
//
// FFmpeg restarts when the client switches quality, and each process starts a
// new SSRC, sequence and timestamp space. The relay rewrites both counters so
// the receiver sees one continuous stream instead of a jump it may discard.
type RTPRelay struct {
	connection  *net.UDPConn
	track       *webrtc.TrackLocalStaticRTP
	logger      *log.Logger
	done        chan struct{}
	closeOnce   sync.Once
	firstPacket sync.Once
	packetCount atomic.Uint64

	// Only the run goroutine touches the rewrite state.
	sourceSSRC      uint32
	haveSource      bool
	sequence        uint16
	timestampOffset uint32
	lastTimestamp   uint32
}

func NewRTPRelay(port int, track *webrtc.TrackLocalStaticRTP, logger *log.Logger) (*RTPRelay, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid RTP port %d", port)
	}
	if track == nil {
		return nil, errors.New("RTP track is required")
	}

	connection, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	})
	if err != nil {
		return nil, fmt.Errorf("listen for FFmpeg RTP on 127.0.0.1:%d: %w", port, err)
	}
	if err := connection.SetReadBuffer(udpReadBuffer); err != nil {
		logger.Printf("enlarge RTP receive buffer: %v", err)
	}

	return &RTPRelay{
		connection: connection,
		track:      track,
		logger:     logger,
		done:       make(chan struct{}),
	}, nil
}

func (r *RTPRelay) Start() {
	go r.run()
}

// Close releases the UDP port and waits until packet forwarding has stopped.
func (r *RTPRelay) Close() {
	r.closeOnce.Do(func() {
		_ = r.connection.Close()
		<-r.done
	})
}

func (r *RTPRelay) run() {
	defer func() {
		r.logger.Printf("desktop RTP relay stopped: packets=%d", r.packetCount.Load())
		close(r.done)
	}()

	buffer := make([]byte, 65_535)
	for {
		packetLength, _, err := r.connection.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			r.logger.Printf("read RTP packet: %v", err)
			continue
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(buffer[:packetLength]); err != nil {
			r.logger.Printf("discard invalid RTP packet: %v", err)
			continue
		}
		r.rewrite(packet)
		if err := r.track.WriteRTP(packet); err != nil {
			r.logger.Printf("forward RTP packet to WebRTC: %v", err)
			continue
		}
		r.packetCount.Add(1)
		r.firstPacket.Do(func() {
			r.logger.Printf("desktop RTP is flowing: first H.264 packet received from FFmpeg")
		})
	}
}

// rewrite maps the packet into the relay's continuous sequence and timestamp
// space. A new source SSRC marks a restarted FFmpeg process.
func (r *RTPRelay) rewrite(packet *rtp.Packet) {
	if !r.haveSource || packet.SSRC != r.sourceSSRC {
		// Continue one 90 kHz frame interval (at 60 FPS) after the last
		// forwarded frame so the receiver's clock keeps moving forward.
		next := r.lastTimestamp + 1500
		if !r.haveSource {
			next = packet.Timestamp
		}
		r.timestampOffset = next - packet.Timestamp
		r.sourceSSRC = packet.SSRC
		r.haveSource = true
	}
	r.sequence++
	packet.SequenceNumber = r.sequence
	packet.Timestamp += r.timestampOffset
	r.lastTimestamp = packet.Timestamp
}
