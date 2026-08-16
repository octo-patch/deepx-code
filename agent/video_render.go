package agent

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Video attachments follow exactly the same contract as image attachments (see
// image_render.go): the canonical history stores nothing but file paths, and the send
// path turns them into content parts at the last moment. The only difference is the
// wire shape — a video part is {"type":"video_url","video_url":{"url":...}} — and the
// capability gate, which comes from model.yaml (`video_input`) rather than a probe.

// videoPlaceholderRe matches the [Video #N] placeholder in a message body; N is the
// 1-based index into ChatMessage.VideoPaths, mirroring [Image #N].
var videoPlaceholderRe = regexp.MustCompile(`\[Video #(\d+)\]`)

// videoFileExtRe matches the container extensions that are worth inlining.
var videoFileExtRe = regexp.MustCompile(`(?i)\.(?:mp4|mov|webm|mkv|m4v|avi)$`)

const (
	// videoReminder / nonVideoReminder are the video counterparts of the vision
	// reminders. Kept in the same language as those so a message never mixes two
	// languages of instructions. Appended to every message carrying a video, at the
	// very end, so the rendering stays deterministic and the prefix cache still hits.
	videoReminder    = "(注:本条消息中的视频已直接提供给你,请直接观看并回答,不要猜测视频内容。)"
	nonVideoReminder = "(注:当前模型看不到视频本身,本条消息只给出了视频文件路径;请如实说明无法观看,不要凭空猜测视频内容。)"

	// maxInlineVideoBytes caps what a single attachment may add to the request body.
	// This is a local safety bound, not an endpoint limit: a base64 data URL is ~4/3 of
	// the file, so an unbounded clip would either blow up the request or stall the turn
	// for minutes. Anything larger degrades to the path-as-text rendering, which is the
	// same fallback used when the model has no video input at all.
	maxInlineVideoBytes = 48 << 20 // 48 MiB
)

// renderConvoMedia renders every attachment on the outgoing copy of the conversation:
// images first (base64 image_url or path + OCR, see renderConvoImages), then videos
// (base64 video_url or path as text). Order matters — the image stage collapses stray
// content parts for non-vision models, so it has to run before video parts exist.
//
// Only the copy that goes out is rendered; the canonical convo keeps holding paths.
func renderConvoMedia(convo []ChatMessage, vision, video bool) []ChatMessage {
	return renderConvoVideos(renderConvoImages(convo, vision), video)
}

// renderConvoVideos is the video stage of renderConvoMedia.
//
//   - video=true  → each [Video #N] becomes a base64 video_url part in place;
//   - video=false → each [Video #N] becomes the video's path as text, so a text-only
//     endpoint is never handed a video part and the user still sees which file was meant.
//
// The last case is the belt-and-braces one: a message that already carries video parts
// from somewhere else is stripped when the current model cannot take video, the same way
// stray image parts are stripped for non-vision models.
func renderConvoVideos(convo []ChatMessage, video bool) []ChatMessage {
	out := make([]ChatMessage, len(convo))
	for i, m := range convo {
		switch {
		case len(m.VideoPaths) > 0 && video:
			out[i] = renderVideoInline(m)
		case len(m.VideoPaths) > 0:
			out[i] = renderVideoPath(m)
		case !video && hasVideoParts(m):
			out[i] = stripVideoParts(m)
		default:
			out[i] = m
		}
	}
	return out
}

// hasVideoParts reports whether the message carries any video_url part.
func hasVideoParts(m ChatMessage) bool {
	for _, p := range m.ContentParts {
		if p.Type == "video_url" {
			return true
		}
	}
	return false
}

// stripVideoParts drops the video parts and keeps everything else untouched.
func stripVideoParts(m ChatMessage) ChatMessage {
	kept := make([]ContentPart, 0, len(m.ContentParts))
	for _, p := range m.ContentParts {
		if p.Type == "video_url" {
			continue
		}
		kept = append(kept, p)
	}
	out := m
	out.ContentParts = kept
	return out
}

// renderVideoInline splits the message's text at every [Video #N] and puts the video
// part exactly where it was referenced, so "compare [Video #1] with [Video #2]" keeps
// its pairing. Videos nobody referenced are appended rather than dropped. Placeholders
// pointing past the attachment list are left alone, and if not a single video could be
// read (file cleaned up, too large) the message falls back to renderVideoPath so the
// model at least learns which file was meant.
func renderVideoInline(m ChatMessage) ChatMessage {
	base := m.ContentParts
	if len(base) == 0 {
		base = []ContentPart{{Type: "text", Text: m.Content}}
	}
	used := make([]bool, len(m.VideoPaths))

	parts := make([]ContentPart, 0, len(base)+len(m.VideoPaths)+1)
	for _, p := range base {
		if p.Type != "text" {
			parts = append(parts, p)
			continue
		}
		parts = append(parts, splitTextAtVideos(p.Text, m.VideoPaths, used)...)
	}
	// Videos whose placeholder was deleted still get sent, at the end.
	for i, path := range m.VideoPaths {
		if used[i] {
			continue
		}
		if part := videoPartFromPath(path); part != nil {
			parts = append(parts, *part)
		}
	}

	hasVideo := false
	for _, p := range parts {
		if p.Type == "video_url" {
			hasVideo = true
			break
		}
	}
	if !hasVideo {
		return renderVideoPath(m)
	}

	parts = append(parts, ContentPart{Type: "text", Text: videoReminder})
	return ChatMessage{Role: m.Role, ContentParts: parts}
}

// splitTextAtVideos cuts one text segment at its [Video #N] placeholders and returns the
// resulting text / video_url parts in order. Empty segments are dropped; an unreadable
// video leaves its placeholder out entirely (the caller falls back when none survived).
func splitTextAtVideos(text string, paths []string, used []bool) []ContentPart {
	locs := videoPlaceholderRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		if seg := strings.TrimSpace(text); seg != "" {
			return []ContentPart{{Type: "text", Text: seg}}
		}
		return nil
	}
	out := make([]ContentPart, 0, len(locs)*2+1)
	prev := 0
	for _, loc := range locs {
		if seg := strings.TrimSpace(text[prev:loc[0]]); seg != "" {
			out = append(out, ContentPart{Type: "text", Text: seg})
		}
		prev = loc[1]
		idx, _ := strconv.Atoi(text[loc[2]:loc[3]])
		if idx < 1 || idx > len(paths) {
			continue
		}
		if part := videoPartFromPath(paths[idx-1]); part != nil {
			out = append(out, *part)
			used[idx-1] = true
		}
	}
	if seg := strings.TrimSpace(text[prev:]); seg != "" {
		out = append(out, ContentPart{Type: "text", Text: seg})
	}
	return out
}

// videoPartFromPath reads the clip and returns one video_url part holding a base64 data
// URL. Returns nil when the extension is not a known video container, the file is gone,
// or it is larger than maxInlineVideoBytes.
func videoPartFromPath(path string) *ContentPart {
	if !videoFileExtRe.MatchString(path) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxInlineVideoBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	url := "data:" + videoMimeByExt(path) + ";base64," + base64.StdEncoding.EncodeToString(data)
	return &ContentPart{Type: "video_url", VideoURL: &VideoURL{URL: url}}
}

// renderVideoPath is the no-video-input rendering: every [Video #N] becomes the file's
// path and the message gets the "you cannot watch this" reminder appended. Content parts
// (an interleaved image rendering) are rewritten in place so the images survive.
func renderVideoPath(m ChatMessage) ChatMessage {
	replace := func(s string) string {
		return videoPlaceholderRe.ReplaceAllStringFunc(s, func(match string) string {
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
	if len(m.ContentParts) > 0 {
		parts := make([]ContentPart, 0, len(m.ContentParts)+1)
		for _, p := range m.ContentParts {
			if p.Type == "video_url" {
				continue
			}
			if p.Type == "text" {
				p.Text = replace(p.Text)
			}
			parts = append(parts, p)
		}
		parts = append(parts, ContentPart{Type: "text", Text: nonVideoReminder})
		return ChatMessage{Role: m.Role, ContentParts: parts}
	}
	replaced := replace(m.Content)
	if strings.TrimSpace(replaced) != "" {
		replaced += "\n\n" + nonVideoReminder
	} else {
		replaced = nonVideoReminder
	}
	return ChatMessage{Role: m.Role, Content: replaced}
}

// isVideoInputUnsupported reports whether the error is an endpoint refusing video input
// (only reachable once base64 has been sent). Deliberately loose about wording, like
// isImageInputUnsupported, because every vendor phrases it differently.
func isVideoInputUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "video input") || strings.Contains(s, "video_url") {
		return true
	}
	return strings.Contains(s, "video") &&
		(strings.Contains(s, "not support") || strings.Contains(s, "no endpoints") || strings.Contains(s, "unsupported"))
}

// videoMimeByExt gives the data URL MIME for a video path; unknown extensions fall back
// to video/mp4, the container every endpoint accepts.
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
