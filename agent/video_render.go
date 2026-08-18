package agent

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// videoPlaceholderRe matches the [Video #N] reference in a message body (N is the Nth entry of
// ChatMessage.VideoPaths), mirroring the [Image #N] placeholder used for attached images.
var videoPlaceholderRe = regexp.MustCompile(`\[Video #(\d+)\]`)

// Trailing notes appended to a message that carries videos, so the model knows whether it is
// expected to watch them or to treat the paths as opaque references. Same idea as the image
// reminders: deterministic per message, appended last, so history rendering stays byte stable and
// the prefix cache still hits.
const (
	videoReminder    = "(Note: the videos of this message are attached inline, watch them directly.)"
	nonVideoReminder = "(Note: this model cannot read video input. Only the video paths above are available, do not guess what the videos show.)"
)

// renderConvoVideos renders messages carrying videos (VideoPaths non empty) into their send form
// according to whether the model of the current turn accepts video input. Every other message is
// returned unchanged. Like renderConvoImages this returns copies and never mutates the canonical
// form of convo (which stores paths only: small history, cache friendly; base64 is produced at the
// moment the request is built). It runs after renderConvoImages, so a message may already be in
// ContentParts form and the video parts are woven into that array.
//
//   - video=true  → [Video #N] is replaced in place by a base64 video_url part, the model watches
//     the clip directly;
//   - video=false → [Video #N] is replaced by the absolute path plus a note, so a text only model
//     is never sent a video part it would reject.
func renderConvoVideos(convo []ChatMessage, video bool) []ChatMessage {
	out := make([]ChatMessage, len(convo))
	for i, m := range convo {
		switch {
		case len(m.VideoPaths) > 0 && video:
			out[i] = renderVideoInline(m)
		case len(m.VideoPaths) > 0:
			out[i] = renderVideoRef(m)
		case !video && hasVideoParts(m.ContentParts):
			// Bottom line: a message carries a video part from somewhere else but the model of this
			// turn does not accept video → drop it, so a video is never sent to a model that would
			// answer with a 4xx.
			out[i] = stripVideoParts(m)
		default:
			out[i] = m
		}
	}
	return out
}

// hasVideoParts reports whether parts contains a video_url part.
func hasVideoParts(parts []ContentPart) bool {
	for _, p := range parts {
		if p.Type == "video_url" {
			return true
		}
	}
	return false
}

// stripVideoParts drops every video_url part and keeps the rest (text and images survive).
// If nothing is left the message would serialize without content, so fall back to the note.
func stripVideoParts(m ChatMessage) ChatMessage {
	kept := make([]ContentPart, 0, len(m.ContentParts))
	for _, p := range m.ContentParts {
		if p.Type == "video_url" {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return ChatMessage{Role: m.Role, Content: nonVideoReminder}
	}
	out := m
	out.ContentParts = kept
	out.VideoPaths = nil
	return out
}

// renderVideoInline interleaves text and videos: every text segment is split at its [Video #N]
// references and the clip is inlined exactly where it was referenced, so "compare [Video #1] with
// [Video #2]" keeps both clips in place. Out of range placeholders are skipped silently and
// attachments that no placeholder referenced are appended at the end so no video is lost.
// If not a single clip could be read the message degrades to the path form instead of losing it.
func renderVideoInline(m ChatMessage) ChatMessage {
	used := make([]bool, len(m.VideoPaths))
	parts := inlineVideoPlaceholders(sendContentParts(m), m.VideoPaths, used)
	for i, p := range m.VideoPaths {
		if used[i] {
			continue
		}
		if part := videoPartFromPath(p); part != nil {
			parts = append(parts, *part)
		}
	}
	if !hasVideoParts(parts) {
		return renderVideoRef(m)
	}
	out := m
	out.Content = ""
	out.ContentParts = append(parts, ContentPart{Type: "text", Text: videoReminder})
	out.VideoPaths = nil
	return out
}

// renderVideoRef is the degraded form for a model without video input: every [Video #N] becomes the
// absolute path of that attachment and the note is appended last. Image parts of the same message
// are preserved — only the text segments are rewritten.
func renderVideoRef(m ChatMessage) ChatMessage {
	replace := func(text string) string {
		return videoPlaceholderRe.ReplaceAllStringFunc(text, func(match string) string {
			sub := videoPlaceholderRe.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			idx, err := strconv.Atoi(sub[1])
			if err != nil || idx < 1 || idx > len(m.VideoPaths) {
				return match
			}
			return m.VideoPaths[idx-1]
		})
	}
	if len(m.ContentParts) == 0 {
		replaced := replace(m.Content)
		if strings.TrimSpace(replaced) != "" {
			replaced += "\n\n" + nonVideoReminder
		} else {
			replaced = nonVideoReminder
		}
		out := m
		out.Content = replaced
		out.VideoPaths = nil
		return out
	}
	parts := make([]ContentPart, 0, len(m.ContentParts)+1)
	for _, p := range m.ContentParts {
		if p.Type == "video_url" {
			continue // a stale inline clip must not reach a model without video input
		}
		if p.Type == "text" {
			p.Text = replace(p.Text)
		}
		parts = append(parts, p)
	}
	out := m
	out.ContentParts = append(parts, ContentPart{Type: "text", Text: nonVideoReminder})
	out.VideoPaths = nil
	return out
}

// sendContentParts normalizes a message to the content array form: an already multimodal message
// keeps its parts, a plain text message becomes a single text part. Empty content yields nil.
func sendContentParts(m ChatMessage) []ContentPart {
	if len(m.ContentParts) > 0 {
		return m.ContentParts
	}
	if strings.TrimSpace(m.Content) != "" {
		return []ContentPart{{Type: "text", Text: m.Content}}
	}
	return nil
}

// inlineVideoPlaceholders splits every text part at its [Video #N] references and inserts the
// matching video part at that position, marking the used attachments in used.
func inlineVideoPlaceholders(in []ContentPart, paths []string, used []bool) []ContentPart {
	out := make([]ContentPart, 0, len(in)+len(paths))
	for _, p := range in {
		if p.Type != "text" || p.Text == "" {
			out = append(out, p)
			continue
		}
		locs := videoPlaceholderRe.FindAllStringSubmatchIndex(p.Text, -1)
		if len(locs) == 0 {
			out = append(out, p)
			continue
		}
		prev := 0
		for _, loc := range locs {
			if seg := strings.TrimSpace(p.Text[prev:loc[0]]); seg != "" {
				out = append(out, ContentPart{Type: "text", Text: seg})
			}
			prev = loc[1]
			idx, err := strconv.Atoi(p.Text[loc[2]:loc[3]])
			if err != nil || idx < 1 || idx > len(paths) {
				continue
			}
			if part := videoPartFromPath(paths[idx-1]); part != nil {
				out = append(out, *part)
				used[idx-1] = true
			}
		}
		if seg := strings.TrimSpace(p.Text[prev:]); seg != "" {
			out = append(out, ContentPart{Type: "text", Text: seg})
		}
	}
	return out
}

// videoPartFromPath reads the clip and encodes it as a base64 data URL video_url part.
// An unreadable path (removed file, stale attachment) returns nil, same as imagePartFromPath.
func videoPartFromPath(path string) *ContentPart {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	url := "data:" + videoMimeByExt(path) + ";base64," + base64.StdEncoding.EncodeToString(data)
	return &ContentPart{Type: "video_url", VideoURL: &VideoURL{URL: url}}
}

// videoMimeByExt maps a file extension to the MIME type of the data URL; mp4 is the fallback
// because it is the container video endpoints accept everywhere.
func videoMimeByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	default:
		return "video/mp4"
	}
}

// isVideoInputUnsupported reports whether the error means "this endpoint/model does not accept video
// input" (only reachable once a video part was actually sent). Matching is loose to tolerate the
// different wordings endpoints use. Mirrors isImageInputUnsupported.
func isVideoInputUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "video input") {
		return true
	}
	return strings.Contains(s, "video") &&
		(strings.Contains(s, "not support") || strings.Contains(s, "no endpoints") || strings.Contains(s, "unsupported"))
}
