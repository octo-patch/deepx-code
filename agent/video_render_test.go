package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeVideoFixture drops a fake clip on disk and returns its path.
func writeVideoFixture(t *testing.T, name string, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, make([]byte, size), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// A video part must serialize as {"type":"video_url","video_url":{"url":...}}, and the
// key must stay out of every request body that has no video.
func TestVideoPartSerialization(t *testing.T) {
	m := ChatMessage{Role: "user", ContentParts: []ContentPart{
		{Type: "text", Text: "what happens here?"},
		{Type: "video_url", VideoURL: &VideoURL{URL: "data:video/mp4;base64,AAA"}},
	}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"type":"video_url"`) || !strings.Contains(s, `"video_url":{"url":"data:video/mp4;base64,AAA"}`) {
		t.Fatalf("expected a video_url part, got: %s", s)
	}

	plain, err := json.Marshal(ChatMessage{Role: "user", Content: "hi"})
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	if strings.Contains(string(plain), "video_url") {
		t.Fatalf("text-only message must not mention video_url, got: %s", plain)
	}
}

// With video input the placeholder becomes a video part in place, the surrounding text is
// preserved, and the reminder is appended last.
func TestRenderConvoMediaInlinesVideo(t *testing.T) {
	path := writeVideoFixture(t, "clip.mp4", 16)
	convo := []ChatMessage{{
		Role:       "user",
		Content:    "before [Video #1] after",
		VideoPaths: []string{path},
	}}

	got := renderConvoMedia(convo, false, true)
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	parts := got[0].ContentParts
	if len(parts) != 4 {
		t.Fatalf("expected text/video/text/reminder, got %d parts: %+v", len(parts), parts)
	}
	if parts[0].Type != "text" || parts[0].Text != "before" {
		t.Fatalf("first part should be the leading text, got %+v", parts[0])
	}
	if parts[1].Type != "video_url" || parts[1].VideoURL == nil {
		t.Fatalf("second part should be the video, got %+v", parts[1])
	}
	if !strings.HasPrefix(parts[1].VideoURL.URL, "data:video/mp4;base64,") {
		t.Fatalf("video should be inlined as a base64 data URL, got %q", parts[1].VideoURL.URL)
	}
	if parts[2].Type != "text" || parts[2].Text != "after" {
		t.Fatalf("third part should be the trailing text, got %+v", parts[2])
	}
	if parts[3].Text != videoReminder {
		t.Fatalf("reminder must come last, got %+v", parts[3])
	}
	if convo[0].ContentParts != nil {
		t.Fatalf("the canonical conversation must not be rewritten")
	}
}

// A video nobody referenced is still sent rather than silently dropped.
func TestRenderConvoMediaKeepsUnreferencedVideo(t *testing.T) {
	path := writeVideoFixture(t, "clip.mov", 8)
	got := renderConvoMedia([]ChatMessage{{
		Role:       "user",
		Content:    "placeholder deleted by the user",
		VideoPaths: []string{path},
	}}, false, true)

	var found string
	for _, p := range got[0].ContentParts {
		if p.Type == "video_url" && p.VideoURL != nil {
			found = p.VideoURL.URL
		}
	}
	if !strings.HasPrefix(found, "data:video/quicktime;base64,") {
		t.Fatalf("expected the unreferenced .mov to be appended with its own MIME, got %q", found)
	}
}

// Without video input the placeholder degrades to the path and no video part is sent.
func TestRenderConvoMediaWithoutVideoInputKeepsPath(t *testing.T) {
	path := writeVideoFixture(t, "clip.mp4", 16)
	got := renderConvoMedia([]ChatMessage{{
		Role:       "user",
		Content:    "look at [Video #1]",
		VideoPaths: []string{path},
	}}, false, false)

	if len(got[0].ContentParts) != 0 {
		t.Fatalf("a model without video input must get no content parts, got %+v", got[0].ContentParts)
	}
	if !strings.Contains(got[0].Content, path) {
		t.Fatalf("expected the path in the text, got %q", got[0].Content)
	}
	if !strings.Contains(got[0].Content, nonVideoReminder) {
		t.Fatalf("expected the no-video reminder, got %q", got[0].Content)
	}
}

// Oversized clips fall back to the path rendering instead of blowing up the request body.
func TestRenderConvoMediaSkipsOversizedVideo(t *testing.T) {
	path := writeVideoFixture(t, "big.mp4", maxInlineVideoBytes+1)
	got := renderConvoMedia([]ChatMessage{{
		Role:       "user",
		Content:    "watch [Video #1]",
		VideoPaths: []string{path},
	}}, false, true)

	if len(got[0].ContentParts) != 0 {
		t.Fatalf("oversized video must not be inlined, got %+v", got[0].ContentParts)
	}
	if !strings.Contains(got[0].Content, path) {
		t.Fatalf("expected the fallback path rendering, got %q", got[0].Content)
	}
}

// A file whose extension is not a known video container is not inlined either.
func TestRenderConvoMediaSkipsUnknownExtension(t *testing.T) {
	path := writeVideoFixture(t, "clip.bin", 16)
	got := renderConvoMedia([]ChatMessage{{
		Role:       "user",
		Content:    "watch [Video #1]",
		VideoPaths: []string{path},
	}}, false, true)

	if len(got[0].ContentParts) != 0 {
		t.Fatalf("unknown container must not be inlined, got %+v", got[0].ContentParts)
	}
	if !strings.Contains(got[0].Content, path) {
		t.Fatalf("expected the fallback path rendering, got %q", got[0].Content)
	}
}

// A missing file behaves the same way: no part, path kept, nothing panics.
func TestRenderConvoMediaMissingVideoFallsBack(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.mp4")
	got := renderConvoMedia([]ChatMessage{{
		Role:       "user",
		Content:    "watch [Video #1]",
		VideoPaths: []string{missing},
	}}, false, true)

	if len(got[0].ContentParts) != 0 {
		t.Fatalf("missing video must not produce a part, got %+v", got[0].ContentParts)
	}
	if !strings.Contains(got[0].Content, missing) {
		t.Fatalf("expected the path in the text, got %q", got[0].Content)
	}
}

// Images and videos on the same message both survive: the image stage runs first and
// hands its VideoPaths on to the video stage.
func TestRenderConvoMediaImageAndVideoTogether(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte("not a real png"), 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	video := writeVideoFixture(t, "clip.webm", 8)

	got := renderConvoMedia([]ChatMessage{{
		Role:       "user",
		Content:    "compare [Image #1] with [Video #1]",
		ImagePaths: []string{img},
		VideoPaths: []string{video},
	}}, true, true)

	var images, videos int
	for _, p := range got[0].ContentParts {
		switch p.Type {
		case "image_url":
			images++
		case "video_url":
			videos++
			if p.VideoURL == nil || !strings.HasPrefix(p.VideoURL.URL, "data:video/webm;base64,") {
				t.Fatalf("unexpected video part: %+v", p)
			}
		}
	}
	if images != 1 || videos != 1 {
		t.Fatalf("expected one image and one video part, got %d/%d: %+v", images, videos, got[0].ContentParts)
	}
}

// Stray video parts are stripped for a model that cannot take video, the same way stray
// image parts are stripped for a non-vision model.
func TestRenderConvoMediaStripsStrayVideoParts(t *testing.T) {
	got := renderConvoMedia([]ChatMessage{{
		Role: "user",
		ContentParts: []ContentPart{
			{Type: "text", Text: "hello"},
			{Type: "video_url", VideoURL: &VideoURL{URL: "data:video/mp4;base64,AAA"}},
		},
	}}, true, false)

	for _, p := range got[0].ContentParts {
		if p.Type == "video_url" {
			t.Fatalf("video part must be stripped when the model has no video input: %+v", got[0].ContentParts)
		}
	}
	if len(got[0].ContentParts) != 1 || got[0].ContentParts[0].Text != "hello" {
		t.Fatalf("the text part must survive, got %+v", got[0].ContentParts)
	}
}

func TestVideoMimeByExt(t *testing.T) {
	cases := map[string]string{
		"a.mp4":     "video/mp4",
		"a.M4V":     "video/mp4",
		"a.mov":     "video/quicktime",
		"a.webm":    "video/webm",
		"a.mkv":     "video/x-matroska",
		"a.avi":     "video/x-msvideo",
		"a.unknown": "video/mp4",
	}
	for path, want := range cases {
		if got := videoMimeByExt(path); got != want {
			t.Errorf("videoMimeByExt(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIsVideoInputUnsupported(t *testing.T) {
	yes := []error{
		errString("HTTP 404: No endpoints found that support video input"),
		errString("this model does not support video"),
		errString("unsupported content part: video_url"),
	}
	for _, err := range yes {
		if !isVideoInputUnsupported(err) {
			t.Errorf("expected a video-unsupported match for %v", err)
		}
	}
	no := []error{
		nil,
		errString("HTTP 429: rate limited"),
		errString("No endpoints found that support image input"),
	}
	for _, err := range no {
		if isVideoInputUnsupported(err) {
			t.Errorf("did not expect a video-unsupported match for %v", err)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
