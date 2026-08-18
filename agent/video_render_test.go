package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempVideo drops a fake clip on disk; the renderer only reads bytes, it never decodes them.
func writeTempVideo(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp video: %v", err)
	}
	return p
}

// A video capable model gets the clip inlined exactly where [Video #N] stood, the surrounding text
// is kept in order and the reminder is appended last.
func TestRenderConvoVideosInline(t *testing.T) {
	clip := writeTempVideo(t, "clip.mp4", "fake-bytes")
	convo := []ChatMessage{{
		Role:       "user",
		Content:    "what happens in [Video #1] here",
		VideoPaths: []string{clip},
	}}

	got := renderConvoVideos(convo, true)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	parts := got[0].ContentParts
	if len(parts) != 4 {
		t.Fatalf("parts = %d (%+v), want 4", len(parts), parts)
	}
	if parts[0].Type != "text" || parts[0].Text != "what happens in" {
		t.Errorf("parts[0] = %+v, want leading text", parts[0])
	}
	if parts[1].Type != "video_url" || parts[1].VideoURL == nil {
		t.Fatalf("parts[1] = %+v, want a video_url part", parts[1])
	}
	if !strings.HasPrefix(parts[1].VideoURL.URL, "data:video/mp4;base64,") {
		t.Errorf("video url = %q, want an mp4 data URL", parts[1].VideoURL.URL)
	}
	if parts[2].Type != "text" || parts[2].Text != "here" {
		t.Errorf("parts[2] = %+v, want trailing text", parts[2])
	}
	if parts[3].Type != "text" || parts[3].Text != videoReminder {
		t.Errorf("parts[3] = %+v, want the reminder last", parts[3])
	}
	// The canonical message must not be touched: rendering only produces a copy.
	if convo[0].Content == "" || len(convo[0].VideoPaths) != 1 {
		t.Errorf("canonical message was mutated: %+v", convo[0])
	}
	if len(got[0].VideoPaths) != 0 {
		t.Errorf("rendered copy still carries VideoPaths: %+v", got[0].VideoPaths)
	}
}

// An attachment that no placeholder references must still be sent (no silent loss).
func TestRenderConvoVideosAppendsUnreferenced(t *testing.T) {
	clip := writeTempVideo(t, "clip.webm", "fake-bytes")
	got := renderConvoVideos([]ChatMessage{{
		Role:       "user",
		Content:    "describe the attachment",
		VideoPaths: []string{clip},
	}}, true)
	if !hasVideoParts(got[0].ContentParts) {
		t.Fatalf("parts = %+v, want a video part appended", got[0].ContentParts)
	}
	for _, p := range got[0].ContentParts {
		if p.Type == "video_url" && !strings.HasPrefix(p.VideoURL.URL, "data:video/webm;base64,") {
			t.Errorf("video url = %q, want a webm data URL", p.VideoURL.URL)
		}
	}
}

// A model without video input never receives a video part: the placeholder becomes the path and the
// note explains that the clip cannot be watched.
func TestRenderConvoVideosDegradesToPath(t *testing.T) {
	clip := writeTempVideo(t, "clip.mp4", "fake-bytes")
	got := renderConvoVideos([]ChatMessage{{
		Role:       "user",
		Content:    "what happens in [Video #1]",
		VideoPaths: []string{clip},
	}}, false)
	if hasVideoParts(got[0].ContentParts) {
		t.Fatalf("a video part reached a model without video input: %+v", got[0].ContentParts)
	}
	if !strings.Contains(got[0].Content, clip) {
		t.Errorf("content = %q, want the absolute path", got[0].Content)
	}
	if !strings.Contains(got[0].Content, nonVideoReminder) {
		t.Errorf("content = %q, want the non-video note", got[0].Content)
	}
}

// Same bottom line for a stray inline part that came from somewhere else: dropped, images survive.
func TestRenderConvoVideosStripsStrayParts(t *testing.T) {
	got := renderConvoVideos([]ChatMessage{{
		Role: "user",
		ContentParts: []ContentPart{
			{Type: "text", Text: "look"},
			{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AA"}},
			{Type: "video_url", VideoURL: &VideoURL{URL: "data:video/mp4;base64,AA"}},
		},
	}}, false)
	if hasVideoParts(got[0].ContentParts) {
		t.Fatalf("stray video part survived: %+v", got[0].ContentParts)
	}
	if len(got[0].ContentParts) != 2 || got[0].ContentParts[1].Type != "image_url" {
		t.Errorf("parts = %+v, want text + image kept", got[0].ContentParts)
	}
}

// A message with an unreadable attachment degrades instead of losing the reference.
func TestRenderConvoVideosUnreadablePath(t *testing.T) {
	got := renderConvoVideos([]ChatMessage{{
		Role:       "user",
		Content:    "see [Video #1]",
		VideoPaths: []string{filepath.Join(t.TempDir(), "gone.mp4")},
	}}, true)
	if hasVideoParts(got[0].ContentParts) {
		t.Fatalf("unreadable clip produced a video part: %+v", got[0].ContentParts)
	}
	if !strings.Contains(got[0].Content, "gone.mp4") {
		t.Errorf("content = %q, want the path kept", got[0].Content)
	}
}

// Messages without attachments are untouched, and a video capable model keeps stray parts as they
// are (nothing to downgrade).
func TestRenderConvoVideosPassThrough(t *testing.T) {
	convo := []ChatMessage{{Role: "user", Content: "plain text"}}
	got := renderConvoVideos(convo, true)
	if got[0].Content != "plain text" || len(got[0].ContentParts) != 0 {
		t.Errorf("message was rewritten: %+v", got[0])
	}
}

// The image pass runs first and must carry VideoPaths over, otherwise a message with both an image
// and a video would lose the clip.
func TestImageRenderKeepsVideoPaths(t *testing.T) {
	clip := writeTempVideo(t, "clip.mp4", "fake-bytes")
	convo := []ChatMessage{{
		Role:       "user",
		Content:    "compare [Image #1] with [Video #1]",
		ImagePaths: []string{"/nonexistent/shot.png"},
		VideoPaths: []string{clip},
	}}
	// vision=false → the image degrades to path + OCR text; the video attachment must survive that
	// pass so the video pass can still inline it.
	got := renderConvoVideos(renderConvoImages(convo, false), true)
	if !hasVideoParts(got[0].ContentParts) {
		t.Fatalf("video attachment was dropped by the image pass: %+v", got[0])
	}
}

// The wire form of a video part is the OpenAI-compatible {"type":"video_url","video_url":{"url":...}}.
func TestVideoPartMarshalJSON(t *testing.T) {
	msg := ChatMessage{Role: "user", ContentParts: []ContentPart{
		{Type: "text", Text: "hi"},
		{Type: "video_url", VideoURL: &VideoURL{URL: "data:video/mp4;base64,AA"}},
	}}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Content []struct {
			Type     string `json:"type"`
			VideoURL *struct {
				URL string `json:"url"`
			} `json:"video_url"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.Content) != 2 {
		t.Fatalf("content = %s, want 2 parts", raw)
	}
	p := wire.Content[1]
	if p.Type != "video_url" || p.VideoURL == nil || p.VideoURL.URL != "data:video/mp4;base64,AA" {
		t.Errorf("video part = %s", raw)
	}
	// A video part must not carry an image_url field (omitempty at work).
	if p.ImageURL != nil {
		t.Errorf("video part carries an image_url field: %s", raw)
	}
}

func TestVideoMimeByExt(t *testing.T) {
	cases := map[string]string{
		"a.mp4":     "video/mp4",
		"a.WEBM":    "video/webm",
		"a.mov":     "video/quicktime",
		"a.mkv":     "video/x-matroska",
		"a.avi":     "video/x-msvideo",
		"a.unknown": "video/mp4",
	}
	for name, want := range cases {
		if got := videoMimeByExt(name); got != want {
			t.Errorf("videoMimeByExt(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestIsVideoInputUnsupported(t *testing.T) {
	if !isVideoInputUnsupported(errTest("HTTP 404: No endpoints found that support video input")) {
		t.Error("explicit video input rejection not detected")
	}
	if !isVideoInputUnsupported(errTest("this model does not support video")) {
		t.Error("loose video rejection not detected")
	}
	if isVideoInputUnsupported(errTest("HTTP 429: rate limited")) {
		t.Error("unrelated error treated as a video rejection")
	}
	if isVideoInputUnsupported(nil) {
		t.Error("nil error treated as a video rejection")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
