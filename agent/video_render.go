package agent

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// videoPlaceholderRe matches the [Video #N] reference inside a message text. N is the 1-based
// index into ChatMessage.VideoPaths, mirroring [Image #N] / ImagePaths.
var videoPlaceholderRe = regexp.MustCompile(`\[Video #(\d+)\]`)

// renderConvoVideos renders the messages that carry videos (VideoPaths non-empty) into the
// form actually sent to the model that runs this turn; every other message is returned as is.
// It returns copies and never touches the canonical convo — the history keeps paths only
// (small, cache friendly) and base64 is produced at request time, exactly like
// renderConvoImages:
//
//   - video=true  → every [Video #N] is replaced in place by a base64 video_url part, so a
//     sentence like "compare [Video #1] and [Video #2]" keeps each video where it is cited;
//   - video=false → [Video #N] degrades back to the absolute file path in the text, so a model
//     without video input learns which file was meant instead of seeing a stray placeholder.
//
// This runs right after renderConvoImages (which is why the image renderers carry VideoPaths
// through), so one message can carry both images and videos: the image stage may already have
// turned the text into ContentParts, and the video stage splits the remaining text parts.
//
// The video=false branch is also the safety rail that mirrors renderConvoImages: a model that
// does not accept video input must never receive a video_url part, otherwise the endpoint
// rejects the whole request instead of just ignoring the attachment.
func renderConvoVideos(convo []ChatMessage, video bool) []ChatMessage {
	out := make([]ChatMessage, len(convo))
	for i, m := range convo {
		switch {
		case len(m.VideoPaths) > 0 && video:
			out[i] = renderVideoInline(m)
		case len(m.VideoPaths) > 0:
			out[i] = renderVideoPathText(m)
		case !video && hasVideoParts(m):
			// Same rail as renderConvoImages: a video_url part of unknown origin is dropped
			// rather than sent to a model that cannot read it.
			out[i] = stripVideoParts(m)
		default:
			out[i] = m
		}
	}
	return out
}

// renderVideoInline interleaves text and video: the text is cut at every [Video #N] and the
// video part is inserted where it was referenced. Videos whose placeholder the user deleted are
// appended at the end so nothing is silently dropped; out-of-range placeholders are skipped.
// If not a single video could be read (file cleaned up, path gone) the message degrades to
// plain paths so a placeholder never reaches the model.
func renderVideoInline(m ChatMessage) ChatMessage {
	src := m.ContentParts
	if len(src) == 0 {
		src = []ContentPart{{Type: "text", Text: m.Content}}
	}
	used := make([]bool, len(m.VideoPaths))
	parts := make([]ContentPart, 0, len(src)+len(m.VideoPaths))
	for _, p := range src {
		if p.Type != "text" || !videoPlaceholderRe.MatchString(p.Text) {
			parts = append(parts, p)
			continue
		}
		parts = append(parts, splitTextOnVideos(p.Text, m.VideoPaths, used)...)
	}
	for i, path := range m.VideoPaths {
		if used[i] {
			continue
		}
		if part := videoPartFromPath(path); part != nil {
			parts = append(parts, *part)
			used[i] = true
		}
	}
	rendered := false
	for _, u := range used {
		if u {
			rendered = true
			break
		}
	}
	if !rendered {
		return renderVideoPathText(m)
	}
	out := m
	out.Content = ""
	out.ContentParts = parts
	out.VideoPaths = nil // consumed by this rendering; the canonical convo still holds them
	return out
}

// splitTextOnVideos cuts one text part at its [Video #N] placeholders and returns the resulting
// text / video parts in order. It marks the videos it consumed in used.
func splitTextOnVideos(text string, paths []string, used []bool) []ContentPart {
	var parts []ContentPart
	prev := 0
	for _, loc := range videoPlaceholderRe.FindAllStringSubmatchIndex(text, -1) {
		if seg := strings.TrimSpace(text[prev:loc[0]]); seg != "" {
			parts = append(parts, ContentPart{Type: "text", Text: seg})
		}
		prev = loc[1]
		idx, _ := strconv.Atoi(text[loc[2]:loc[3]])
		if idx < 1 || idx > len(paths) {
			continue
		}
		if part := videoPartFromPath(paths[idx-1]); part != nil {
			parts = append(parts, *part)
			used[idx-1] = true
		}
	}
	if seg := strings.TrimSpace(text[prev:]); seg != "" {
		parts = append(parts, ContentPart{Type: "text", Text: seg})
	}
	return parts
}

// renderVideoPathText replaces every [Video #N] with the absolute file path and drops any
// video_url part. Used for models without video input: the request stays valid and the model
// can still be told about the file by path.
func renderVideoPathText(m ChatMessage) ChatMessage {
	replace := func(text string) string {
		return videoPlaceholderRe.ReplaceAllStringFunc(text, func(match string) string {
			sub := videoPlaceholderRe.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			idx, _ := strconv.Atoi(sub[1])
			if idx < 1 || idx > len(m.VideoPaths) {
				return match
			}
			return m.VideoPaths[idx-1]
		})
	}
	out := m
	out.VideoPaths = nil
	out.Content = replace(m.Content)
	if len(m.ContentParts) > 0 {
		parts := make([]ContentPart, 0, len(m.ContentParts))
		for _, p := range m.ContentParts {
			if p.Type == "video_url" {
				continue
			}
			if p.Type == "text" {
				p.Text = replace(p.Text)
			}
			parts = append(parts, p)
		}
		out.ContentParts = parts
	}
	return out
}

// hasVideoParts reports whether the message already carries a video_url part.
func hasVideoParts(m ChatMessage) bool {
	for _, p := range m.ContentParts {
		if p.Type == "video_url" {
			return true
		}
	}
	return false
}

// stripVideoParts removes the video parts and keeps everything else. Unlike stripImageParts it
// does not collapse the message to text: dropping video must not also throw away the images a
// vision model can still read.
func stripVideoParts(m ChatMessage) ChatMessage {
	parts := make([]ContentPart, 0, len(m.ContentParts))
	for _, p := range m.ContentParts {
		if p.Type == "video_url" {
			continue
		}
		parts = append(parts, p)
	}
	out := m
	out.ContentParts = parts
	return out
}

// videoPartFromPath reads the file and encodes it as a base64 data URL video_url part;
// unreadable paths (cleaned up / stale) return nil so the caller can degrade.
func videoPartFromPath(path string) *ContentPart {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	url := "data:" + videoMimeByExt(path) + ";base64," + base64.StdEncoding.EncodeToString(data)
	return &ContentPart{Type: "video_url", VideoURL: &VideoURL{URL: url}}
}

// videoMimeByExt gives the data URL MIME type for a video path. mp4 is the fallback because it
// is the most common container among the extensions that can be attached.
func videoMimeByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	default:
		return "video/mp4"
	}
}
