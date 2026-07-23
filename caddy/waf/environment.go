package waf

import (
	"net/http"
	"strings"
)

const browserEnvironmentVersion = 1

type browserEnvironment struct {
	Version             int      `json:"v"`
	UserAgent           string   `json:"user_agent"`
	Platform            string   `json:"platform,omitempty"`
	Vendor              string   `json:"vendor,omitempty"`
	Languages           []string `json:"languages"`
	Timezone            string   `json:"timezone,omitempty"`
	Webdriver           bool     `json:"webdriver"`
	Cookies             bool     `json:"cookies"`
	Worker              bool     `json:"worker"`
	WebAssembly         bool     `json:"webassembly"`
	Crypto              bool     `json:"crypto"`
	Canvas              bool     `json:"canvas"`
	WebGL               bool     `json:"webgl"`
	WebGLVendor         string   `json:"webgl_vendor,omitempty"`
	WebGLRenderer       string   `json:"webgl_renderer,omitempty"`
	LocalStorage        bool     `json:"local_storage"`
	SessionStorage      bool     `json:"session_storage"`
	IndexedDB           bool     `json:"indexed_db"`
	HardwareConcurrency int      `json:"hardware_concurrency"`
	DeviceMemory        float64  `json:"device_memory,omitempty"`
	TouchPoints         int      `json:"touch_points"`
	ScreenWidth         int      `json:"screen_width"`
	ScreenHeight        int      `json:"screen_height"`
	ColorDepth          int      `json:"color_depth"`
	OuterWidth          int      `json:"outer_width"`
	OuterHeight         int      `json:"outer_height"`
	AutomationSignals   []string `json:"automation_signals"`
	NativeTampering     []string `json:"native_tampering"`
}

type environmentAssessment struct {
	Accepted bool
	Score    int
	Reasons  []string
}

// assessBrowserEnvironment combines high-confidence automation disclosures
// with softer consistency signals. Client-side values are spoofable, so this
// is a risk layer rather than remote attestation.
func assessBrowserEnvironment(r *http.Request, environment browserEnvironment) environmentAssessment {
	result := environmentAssessment{Accepted: true}
	hard := func(reason string) {
		result.Accepted = false
		result.Reasons = append(result.Reasons, reason)
	}
	soft := func(score int, reason string) {
		result.Score += score
		result.Reasons = append(result.Reasons, reason)
	}

	if environment.Version != browserEnvironmentVersion {
		hard("unsupported-environment-version")
	}
	if environment.UserAgent == "" || environment.UserAgent != r.UserAgent() {
		hard("user-agent-mismatch")
	}
	userAgent := strings.ToLower(environment.UserAgent)
	if environment.Webdriver {
		hard("webdriver")
	}
	if len(environment.AutomationSignals) > 0 {
		hard("automation-global")
	}
	if strings.Contains(userAgent, "headlesschrome") || strings.Contains(userAgent, "phantomjs") || strings.Contains(userAgent, "slimerjs") {
		hard("headless-user-agent")
	}
	if !environment.Cookies {
		hard("cookies-disabled")
	}
	if !environment.Worker || !environment.WebAssembly {
		soft(1, "limited-runtime")
	}
	if !environment.Crypto {
		soft(1, "missing-webcrypto")
	}
	if !environment.Canvas {
		soft(1, "missing-canvas")
	}
	if !environment.WebGL {
		soft(1, "missing-webgl")
	}
	renderer := strings.ToLower(environment.WebGLRenderer)
	if strings.Contains(renderer, "swiftshader") || strings.Contains(renderer, "llvmpipe") || strings.Contains(renderer, "software") {
		soft(1, "software-renderer")
	}
	if !environment.LocalStorage && !environment.SessionStorage && !environment.IndexedDB {
		soft(1, "storage-unavailable")
	}
	if environment.HardwareConcurrency < 1 {
		soft(1, "missing-hardware-concurrency")
	}
	if environment.ScreenWidth < 1 || environment.ScreenHeight < 1 || environment.ColorDepth < 1 {
		soft(2, "invalid-screen")
	}
	if environment.OuterWidth < 1 || environment.OuterHeight < 1 {
		soft(1, "zero-window-size")
	}
	if len(environment.Languages) == 0 {
		soft(1, "missing-languages")
	}
	if environment.Platform == "" {
		soft(1, "missing-platform")
	}
	if environment.Timezone == "" {
		soft(1, "missing-timezone")
	}
	if strings.Contains(userAgent, "chrome/") && !strings.Contains(userAgent, "edg/") && !strings.Contains(userAgent, "opr/") && environment.Vendor != "Google Inc." {
		soft(1, "chromium-vendor-mismatch")
	}
	if strings.Contains(userAgent, "safari/") && !strings.Contains(userAgent, "chrome/") && environment.Vendor != "Apple Computer, Inc." {
		soft(1, "safari-vendor-mismatch")
	}
	if strings.Contains(userAgent, "mobile") && environment.TouchPoints < 1 {
		soft(1, "mobile-touch-mismatch")
	}
	if len(environment.NativeTampering) > 0 {
		soft(2, "native-api-tampering")
	}
	if language := strings.TrimSpace(r.Header.Get("Accept-Language")); language != "" && len(environment.Languages) == 0 {
		soft(1, "accept-language-mismatch")
	}
	if result.Score >= 6 {
		result.Accepted = false
		result.Reasons = append(result.Reasons, "risk-threshold")
	}
	return result
}
