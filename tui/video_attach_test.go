package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// touch creates an empty file under dir and returns its absolute path.
func touch(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// "@clip.mp4" becomes a [Video #N] placeholder + an attachment; non-video mentions are left for
// resolveFileMentions to backquote.
func TestExtractVideoMentions(t *testing.T) {
	ws := t.TempDir()
	clip := touch(t, ws, "clip.mp4")
	touch(t, ws, "notes.md")

	text, paths := extractVideoMentions("watch @clip.mp4 and read @notes.md", ws)
	if want := "watch [Video #1] and read @notes.md"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if want := []string{clip}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

// Numbering follows the order the placeholders appear in, and the same file reused twice keeps a
// single number so it is not encoded twice.
func TestExtractVideoMentionsNumbering(t *testing.T) {
	ws := t.TempDir()
	first := touch(t, ws, "a.mp4")
	second := touch(t, ws, "b.mov")

	text, paths := extractVideoMentions("@a.mp4 then @b.mov then @a.mp4 again", ws)
	if want := "[Video #1] then [Video #2] then [Video #1] again"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if want := []string{first, second}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

// A mention that points nowhere, or at a directory, is not an attachment.
func TestExtractVideoMentionsIgnoresMissingAndDirs(t *testing.T) {
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, "movies.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}
	text, paths := extractVideoMentions("@gone.mp4 @movies.mp4", ws)
	if text != "@gone.mp4 @movies.mp4" {
		t.Errorf("text = %q, want it untouched", text)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want none", paths)
	}
}

func TestVideoFileExtRe(t *testing.T) {
	for _, ok := range []string{"a.mp4", "a.MP4", "a.mov", "a.webm", "a.mkv", "a.avi"} {
		if !videoFileExtRe.MatchString(ok) {
			t.Errorf("%q should match", ok)
		}
	}
	for _, no := range []string{"a.png", "a.mp4.txt", "a.md", "amp4"} {
		if videoFileExtRe.MatchString(no) {
			t.Errorf("%q should not match", no)
		}
	}
}
