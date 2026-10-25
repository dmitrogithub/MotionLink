package rtc

import (
	"testing"

	"github.com/pion/rtp"
)

func TestRewriteKeepsStreamContinuousAcrossRestart(t *testing.T) {
	relay := &RTPRelay{}
	first := []*rtp.Packet{
		{Header: rtp.Header{SSRC: 1, SequenceNumber: 500, Timestamp: 9000}},
		{Header: rtp.Header{SSRC: 1, SequenceNumber: 501, Timestamp: 10500}},
	}
	for _, packet := range first {
		relay.rewrite(packet)
	}
	if first[1].SequenceNumber != first[0].SequenceNumber+1 || first[1].Timestamp != 10500 {
		t.Fatalf("first stream altered unexpectedly: %+v", first[1].Header)
	}

	// FFmpeg restarted with a new SSRC and random counters.
	restarted := &rtp.Packet{Header: rtp.Header{SSRC: 2, SequenceNumber: 7, Timestamp: 123}}
	relay.rewrite(restarted)
	if restarted.SequenceNumber != first[1].SequenceNumber+1 {
		t.Fatalf("sequence jumped to %d after restart", restarted.SequenceNumber)
	}
	if restarted.Timestamp != 10500+1500 {
		t.Fatalf("timestamp is %d after restart, want %d", restarted.Timestamp, 10500+1500)
	}

	next := &rtp.Packet{Header: rtp.Header{SSRC: 2, SequenceNumber: 8, Timestamp: 123 + 3000}}
	relay.rewrite(next)
	if next.Timestamp != restarted.Timestamp+3000 {
		t.Fatalf("timestamps of the new stream are not offset consistently")
	}
}
