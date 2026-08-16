package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// extractVideoMentions must turn "@some.mp4" into a [Video #N] placeholder plus an
// absolute path, and leave every other mention for resolveFileMentions.
func TestExtractVideoMentions(t *testing.T) {
	wd := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(wd, name)
		if err := os.WriteFile(p, []byte("data"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	clip := write("clip.mp4")
	second := write("b.MOV")
	write("notes.md")
	if err := os.Mkdir(filepath.Join(wd, "movies.mp4"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cases := []struct {
		name      string
		text      string
		wantText  string
		wantPaths []string
	}{
		{
			name:      "single video mention",
			text:      "summarize @clip.mp4 please",
			wantText:  "summarize [Video #1] please",
			wantPaths: []string{clip},
		},
		{
			name:      "numbering follows mention order",
			text:      "@b.MOV then @clip.mp4",
			wantText:  "[Video #1] then [Video #2]",
			wantPaths: []string{second, clip},
		},
		{
			name:      "non-video mention untouched",
			text:      "read @notes.md",
			wantText:  "read @notes.md",
			wantPaths: nil,
		},
		{
			name:      "missing file untouched",
			text:      "watch @gone.mp4",
			wantText:  "watch @gone.mp4",
			wantPaths: nil,
		},
		{
			name:      "directory with a video extension untouched",
			text:      "list @movies.mp4",
			wantText:  "list @movies.mp4",
			wantPaths: nil,
		},
		{
			name:      "no mention at all",
			text:      "plain question",
			wantText:  "plain question",
			wantPaths: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotText, gotPaths := extractVideoMentions(c.text, wd)
			if gotText != c.wantText {
				t.Fatalf("text = %q, want %q", gotText, c.wantText)
			}
			if !reflect.DeepEqual(gotPaths, c.wantPaths) {
				t.Fatalf("paths = %v, want %v", gotPaths, c.wantPaths)
			}
		})
	}
}

// A video mention must not also be rewritten into a Read reference: the placeholder is
// what pairs the message text with ChatMessage.VideoPaths.
func TestExtractVideoMentionsRunsBeforeResolve(t *testing.T) {
	wd := t.TempDir()
	clip := filepath.Join(wd, "clip.mp4")
	if err := os.WriteFile(clip, []byte("data"), 0644); err != nil {
		t.Fatalf("write clip: %v", err)
	}
	text, paths := extractVideoMentions("check @clip.mp4", wd)
	if got := resolveFileMentions(text, wd); got != "check [Video #1]" {
		t.Fatalf("resolveFileMentions must leave the placeholder alone, got %q", got)
	}
	if len(paths) != 1 || paths[0] != clip {
		t.Fatalf("paths = %v, want [%s]", paths, clip)
	}
}
