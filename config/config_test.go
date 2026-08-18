package config

import (
	"reflect"
	"testing"
)

// DefaultFor 按供应商给出正确的 base_url / 模型 / max_tokens。
// 关键回归:base_url 只到域名(agent 再追加 /chat/completions),mimo 的 max_tokens=131072。
func TestDefaultForProviders(t *testing.T) {
	cases := []struct {
		provider    string
		wantBaseURL string
		wantFlash   string
		wantPro     string
		wantMaxTok  int
		wantCtxWin  int
	}{
		{"deepseek", "https://api.deepseek.com", "deepseek-v4-flash", "deepseek-v4-pro", 393216, 1_048_576},
		{"mimo", "https://api.xiaomimimo.com/v1", "mimo-v2.5", "mimo-v2.5-pro", 131072, 1_048_576},
		{"unknown-provider", "https://api.deepseek.com", "deepseek-v4-flash", "deepseek-v4-pro", 393216, 1_048_576}, // 回退 deepseek
	}
	for _, c := range cases {
		cfg := DefaultFor(c.provider, "sk-test")
		if cfg.Flash.BaseURL != c.wantBaseURL || cfg.Pro.BaseURL != c.wantBaseURL {
			t.Errorf("%s base_url = %q/%q, want %q", c.provider, cfg.Flash.BaseURL, cfg.Pro.BaseURL, c.wantBaseURL)
		}
		if cfg.Flash.Model != c.wantFlash || cfg.Pro.Model != c.wantPro {
			t.Errorf("%s models = %q/%q, want %q/%q", c.provider, cfg.Flash.Model, cfg.Pro.Model, c.wantFlash, c.wantPro)
		}
		if cfg.Flash.MaxTokens != c.wantMaxTok || cfg.Pro.MaxTokens != c.wantMaxTok {
			t.Errorf("%s max_tokens = %d/%d, want %d", c.provider, cfg.Flash.MaxTokens, cfg.Pro.MaxTokens, c.wantMaxTok)
		}
		if cfg.Flash.ContextWindow != c.wantCtxWin || cfg.Pro.ContextWindow != c.wantCtxWin {
			t.Errorf("%s context_window = %d/%d, want %d", c.provider, cfg.Flash.ContextWindow, cfg.Pro.ContextWindow, c.wantCtxWin)
		}
	}
}

// defaultMaxTokens 给旧 yaml 兜底:deepseek 走 384K,其它(含 mimo)保守 131072。
func TestDefaultMaxTokens(t *testing.T) {
	if got := defaultMaxTokens("deepseek-v4-pro"); got != 393216 {
		t.Errorf("deepseek max_tokens = %d, want 393216", got)
	}
	if got := defaultMaxTokens("mimo-v2.5"); got != 131072 {
		t.Errorf("mimo max_tokens = %d, want 131072", got)
	}
}

// SupportsVideoInput declares which model ids accept video input; the lookup ignores case and
// blanks (the id can be typed by hand into model.yaml) and unlisted ids stay text/image only.
func TestSupportsVideoInput(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"MiniMax-M3", true},
		{"minimax-m3", true},
		{"  MiniMax-M3  ", true},
		{"MiniMax-M2.7", false},
		{"", false},
		{"some-unlisted-model", false},
	}
	for _, c := range cases {
		if got := SupportsVideoInput(c.model); got != c.want {
			t.Errorf("SupportsVideoInput(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

// Video is a runtime capability bit, never read from or written to model.yaml.
func TestVideoIsNotAConfigField(t *testing.T) {
	f, ok := reflect.TypeOf(ModelEntry{}).FieldByName("Video")
	if !ok {
		t.Fatal("ModelEntry has no Video field")
	}
	if tag := f.Tag.Get("yaml"); tag != "-" {
		t.Errorf("Video yaml tag = %q, want %q", tag, "-")
	}
}
