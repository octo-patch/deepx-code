package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempVideo drops a fake video file on disk and returns its path. The render path only
// reads bytes and looks at the extension, so the content does not have to be a real container.
func writeTempVideo(t *testing.T, name string, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A video-capable model gets the videos interleaved at their [Video #N] positions, encoded as
// base64 video_url parts.
func TestRenderConvoVideosInlinesVideoURLParts(t *testing.T) {
	first := writeTempVideo(t, "one.mp4", "aaa")
	second := writeTempVideo(t, "two.webm", "bbb")

	convo := []ChatMessage{{
		Role:       "user",
		Content:    "compare [Video #1] and [Video #2] please",
		VideoPaths: []string{first, second},
	}}
	out := renderConvoVideos(convo, true)
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1", len(out))
	}
	got := out[0]
	if got.Content != "" {
		t.Errorf("Content = %q, want empty once parts are built", got.Content)
	}
	if got.VideoPaths != nil {
		t.Errorf("VideoPaths = %v, want nil after rendering", got.VideoPaths)
	}
	wantTypes := []string{"text", "video_url", "text", "video_url", "text"}
	if len(got.ContentParts) != len(wantTypes) {
		t.Fatalf("parts = %d, want %d (%+v)", len(got.ContentParts), len(wantTypes), got.ContentParts)
	}
	for i, want := range wantTypes {
		if got.ContentParts[i].Type != want {
			t.Errorf("part %d type = %q, want %q", i, got.ContentParts[i].Type, want)
		}
	}
	if !strings.HasPrefix(got.ContentParts[1].VideoURL.URL, "data:video/mp4;base64,") {
		t.Errorf("first video URL = %q, want an mp4 data URL", got.ContentParts[1].VideoURL.URL)
	}
	if !strings.HasPrefix(got.ContentParts[3].VideoURL.URL, "data:video/webm;base64,") {
		t.Errorf("second video URL = %q, want a webm data URL", got.ContentParts[3].VideoURL.URL)
	}
	if got.ContentParts[0].Text != "compare" || got.ContentParts[4].Text != "please" {
		t.Errorf("text parts = %q / %q", got.ContentParts[0].Text, got.ContentParts[4].Text)
	}
}

// A video whose placeholder the user deleted is still appended, so the attachment is not lost.
func TestRenderConvoVideosAppendsUnreferencedVideo(t *testing.T) {
	p := writeTempVideo(t, "clip.mp4", "aaa")
	out := renderConvoVideos([]ChatMessage{{
		Role:       "user",
		Content:    "no placeholder here",
		VideoPaths: []string{p},
	}}, true)
	var videos int
	for _, part := range out[0].ContentParts {
		if part.Type == "video_url" {
			videos++
		}
	}
	if videos != 1 {
		t.Fatalf("video parts = %d, want 1 (%+v)", videos, out[0].ContentParts)
	}
}

// A model without video input must never see a video_url part: the placeholder degrades back to
// the file path so the request stays valid.
func TestRenderConvoVideosDegradesForTextOnlyModel(t *testing.T) {
	p := writeTempVideo(t, "clip.mp4", "aaa")
	out := renderConvoVideos([]ChatMessage{{
		Role:       "user",
		Content:    "look at [Video #1]",
		VideoPaths: []string{p},
	}}, false)
	got := out[0]
	if hasVideoParts(got) {
		t.Fatalf("a text-only model must not receive video parts: %+v", got.ContentParts)
	}
	if got.Content != "look at "+p {
		t.Errorf("Content = %q, want the placeholder replaced by %q", got.Content, p)
	}
	if got.VideoPaths != nil {
		t.Errorf("VideoPaths = %v, want nil after rendering", got.VideoPaths)
	}
}

// An unreadable path degrades instead of leaving a bare [Video #N] in front of the model.
func TestRenderConvoVideosDegradesWhenFileMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.mp4")
	out := renderConvoVideos([]ChatMessage{{
		Role:       "user",
		Content:    "check [Video #1]",
		VideoPaths: []string{missing},
	}}, true)
	if hasVideoParts(out[0]) {
		t.Fatalf("unreadable video must not produce a part: %+v", out[0].ContentParts)
	}
	if strings.Contains(out[0].Content, "[Video #1]") {
		t.Errorf("Content = %q, placeholder should be gone", out[0].Content)
	}
}

// Images run first, so a message carrying both ends up with image and video parts side by side.
func TestRenderConvoVideosComposesWithImageParts(t *testing.T) {
	video := writeTempVideo(t, "clip.mp4", "aaa")
	convo := []ChatMessage{{
		Role: "user",
		ContentParts: []ContentPart{
			{Type: "text", Text: "watch [Video #1] after this"},
			{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AAA"}},
		},
		VideoPaths: []string{video},
	}}
	got := renderConvoVideos(convo, true)[0]
	wantTypes := []string{"text", "video_url", "text", "image_url"}
	if len(got.ContentParts) != len(wantTypes) {
		t.Fatalf("parts = %d, want %d (%+v)", len(got.ContentParts), len(wantTypes), got.ContentParts)
	}
	for i, want := range wantTypes {
		if got.ContentParts[i].Type != want {
			t.Errorf("part %d type = %q, want %q", i, got.ContentParts[i].Type, want)
		}
	}
}

// The image stage must carry VideoPaths through, otherwise the video stage that runs after it
// finds nothing to render.
func TestRenderConvoImagesKeepsVideoPaths(t *testing.T) {
	for _, vision := range []bool{true, false} {
		convo := []ChatMessage{{
			Role:       "user",
			Content:    "text only, [Video #1]",
			VideoPaths: []string{"/tmp/clip.mp4"},
		}}
		if got := renderConvoImages(convo, vision)[0]; len(got.VideoPaths) != 1 {
			t.Fatalf("vision=%v dropped VideoPaths: %+v", vision, got)
		}
	}
}

// stripVideoParts drops videos of unknown origin but keeps the images a vision model can read.
func TestStripVideoPartsKeepsImages(t *testing.T) {
	m := ChatMessage{Role: "user", ContentParts: []ContentPart{
		{Type: "text", Text: "hi"},
		{Type: "video_url", VideoURL: &VideoURL{URL: "data:video/mp4;base64,AAA"}},
		{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AAA"}},
	}}
	got := renderConvoVideos([]ChatMessage{m}, false)[0]
	wantTypes := []string{"text", "image_url"}
	if len(got.ContentParts) != len(wantTypes) {
		t.Fatalf("parts = %+v, want %v", got.ContentParts, wantTypes)
	}
	for i, want := range wantTypes {
		if got.ContentParts[i].Type != want {
			t.Errorf("part %d type = %q, want %q", i, got.ContentParts[i].Type, want)
		}
	}
}

// A video_url part serializes to the OpenAI-compatible {"type":"video_url","video_url":{"url":…}}
// shape, and text / image parts must not grow an empty video_url key.
func TestContentPartVideoURLMarshalJSON(t *testing.T) {
	body, err := json.Marshal(ChatMessage{Role: "user", ContentParts: []ContentPart{
		{Type: "text", Text: "hi"},
		{Type: "video_url", VideoURL: &VideoURL{URL: "data:video/mp4;base64,AAA"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, `{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAA"}}`) {
		t.Errorf("marshalled video part missing from %s", got)
	}
	if strings.Contains(got, `{"type":"text","text":"hi","video_url"`) {
		t.Errorf("text part must not carry a video_url key: %s", got)
	}
}

func TestVideoMimeByExt(t *testing.T) {
	cases := map[string]string{
		"/a/clip.mp4":  "video/mp4",
		"/a/clip.MOV":  "video/quicktime",
		"/a/clip.webm": "video/webm",
		"/a/clip.mkv":  "video/x-matroska",
		"/a/clip.avi":  "video/x-msvideo",
		"/a/clip.bin":  "video/mp4",
	}
	for path, want := range cases {
		if got := videoMimeByExt(path); got != want {
			t.Errorf("videoMimeByExt(%q) = %q, want %q", path, got, want)
		}
	}
}
