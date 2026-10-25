package ffmpeg

import "fmt"

// Profile is a capture/encode preset the client can choose. Every profile uses
// H.264 constrained baseline so phone and desktop hardware decoders accept it
// with the SDP the host negotiates.
type Profile struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	FPS         int    `json:"fps"`
	MaxWidth    int    `json:"maxWidth"`
	BitrateKbps int    `json:"bitrateKbps"`
	// Quality presets trade encoder speed for detail.
	nvencPreset string
	x264Preset  string
}

const (
	ProfileFPS30   = "fps30"
	ProfileFPS60   = "fps60"
	ProfileQuality = "quality"
	DefaultProfile = ProfileFPS60
)

var profiles = []Profile{
	{
		ID:          ProfileFPS30,
		Label:       "30 FPS",
		Description: "Saves bandwidth and CPU; best for mobile data and weak PCs",
		FPS:         30,
		MaxWidth:    1600,
		BitrateKbps: 4_000,
		nvencPreset: "p1",
		x264Preset:  "ultrafast",
	},
	{
		ID:          ProfileFPS60,
		Label:       "60 FPS",
		Description: "Smooth motion for games and video, 1080p",
		FPS:         60,
		MaxWidth:    1920,
		BitrateKbps: 10_000,
		nvencPreset: "p1",
		x264Preset:  "ultrafast",
	},
	{
		ID:          ProfileQuality,
		Label:       "High quality",
		Description: "Native resolution, 30 FPS and a high bitrate for sharp text; needs a fast network",
		FPS:         30,
		MaxWidth:    3840,
		BitrateKbps: 25_000,
		nvencPreset: "p4",
		x264Preset:  "superfast",
	},
}

// Profiles returns the presets in display order.
func Profiles() []Profile {
	return append([]Profile(nil), profiles...)
}

// LookupProfile returns the named profile, or the default one for "".
func LookupProfile(id string) (Profile, error) {
	if id == "" {
		id = DefaultProfile
	}
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return Profile{}, fmt.Errorf("unknown quality profile %q", id)
}

func (p Profile) gop() int {
	// Half-second keyframes let a client recover quickly after packet loss.
	return max(p.FPS/2, 15)
}

func (p Profile) bitrate() string {
	return fmt.Sprintf("%dk", p.BitrateKbps)
}

func (p Profile) bufferSize() string {
	// About two frames of buffer keeps the rate control tight for low latency.
	return fmt.Sprintf("%dk", max(p.BitrateKbps*2/p.FPS, 200))
}

func (p Profile) videoFilter() string {
	// Downscale only when the desktop is wider than the profile allows, and
	// always produce even dimensions, which H.264 encoders require.
	return fmt.Sprintf("scale='min(%d,iw)':-2:flags=bicubic,scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p", p.MaxWidth)
}
