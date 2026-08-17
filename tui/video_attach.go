package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Video attachment: an "@path" mention that points at a real local video file is turned into a
// [Video #N] placeholder plus an attachment on the outgoing message, instead of the backquoted
// path resolveFileMentions produces for ordinary files (a video is useless to the Read tool).
// From there it travels the same canonical route as a pasted image — the history stores paths
// only and agent.renderConvoVideos emits base64 video_url parts at request time, and only for
// models that accept video input.

// videoFileExtRe matches the video containers that can be attached (extension at the end).
var videoFileExtRe = regexp.MustCompile(`(?i)\.(?:mp4|mov|webm|mkv|avi)$`)

// extractVideoMentions rewrites every "@path" mention that resolves to an existing local video
// file into a "[Video #N]" placeholder and returns the rewritten text plus the absolute paths in
// placeholder order. Mentions that are not videos, or that point nowhere, are left untouched for
// resolveFileMentions to handle. The same file mentioned twice reuses one placeholder number, so
// it is not encoded and uploaded twice.
func extractVideoMentions(text, workspace string) (string, []string) {
	var paths []string
	seen := map[string]int{}
	out := fileMentionRe.ReplaceAllStringFunc(text, func(match string) string {
		rel := strings.TrimPrefix(match, "@")
		if !videoFileExtRe.MatchString(rel) {
			return match
		}
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workspace, rel)
		}
		abs = filepath.Clean(abs)
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return match
		}
		n, ok := seen[abs]
		if !ok {
			paths = append(paths, abs)
			n = len(paths)
			seen[abs] = n
		}
		return fmt.Sprintf("[Video #%d]", n)
	})
	return out, paths
}
