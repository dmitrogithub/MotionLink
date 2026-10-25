package ffmpeg

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestLookupProfile(t *testing.T) {
	profile, err := LookupProfile("")
	if err != nil || profile.ID != DefaultProfile {
		t.Fatalf("empty id should select the default profile, got %+v %v", profile, err)
	}
	if _, err := LookupProfile("8k"); err == nil {
		t.Fatalf("unknown profile accepted")
	}
}

func TestCaptureArgumentsFollowProfile(t *testing.T) {
	for _, encoder := range []string{"h264_nvenc", "libx264"} {
		for _, profile := range Profiles() {
			arguments := captureArguments(encoder, 5004, profile)
			joined := strings.Join(arguments, " ")
			framerate := arguments[slices.Index(arguments, "-framerate")+1]
			if framerate != strconv.Itoa(profile.FPS) {
				t.Fatalf("%s/%s: unexpected framerate %q", encoder, profile.ID, framerate)
			}
			if !strings.Contains(joined, "-b:v "+profile.bitrate()) {
				t.Fatalf("%s/%s: bitrate missing in %s", encoder, profile.ID, joined)
			}
			if !strings.Contains(joined, "baseline") || !strings.HasSuffix(joined, "rtp://127.0.0.1:5004?pkt_size=1200&localaddr=127.0.0.1") {
				t.Fatalf("%s/%s: stream must stay baseline RTP: %s", encoder, profile.ID, joined)
			}
		}
	}
}
