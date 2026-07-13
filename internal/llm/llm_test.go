package llm

import "testing"

func TestResolveBaseURL(t *testing.T) {
	cases := []struct {
		cfg     Config
		want    string
		wantErr bool
	}{
		{Config{Provider: "openai"}, "https://api.openai.com/v1", false},
		{Config{Provider: "deepseek"}, "https://api.deepseek.com/v1", false},
		{Config{Provider: "anthropic"}, "https://api.anthropic.com", false},
		{Config{Provider: "ollama"}, "http://127.0.0.1:11434/v1", false},
		{Config{Provider: "custom"}, "", true}, // needs explicit base
		{Config{Provider: "custom", BaseURL: "http://x:8080/v1"}, "http://x:8080/v1", false},
		{Config{Provider: "openai", BaseURL: "https://proxy/v1/"}, "https://proxy/v1", false}, // override + trailing slash trimmed
	}
	for _, c := range cases {
		got, err := c.cfg.resolveBaseURL()
		if (err != nil) != c.wantErr {
			t.Errorf("%+v: err=%v wantErr=%v", c.cfg, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("%+v: got %q want %q", c.cfg, got, c.want)
		}
	}
}

func TestIsAnthropic(t *testing.T) {
	if !(Config{Provider: "anthropic"}).isAnthropic() {
		t.Error("anthropic not detected")
	}
	if !(Config{Provider: "Anthropic"}).isAnthropic() {
		t.Error("case-insensitive match failed")
	}
	if (Config{Provider: "openai"}).isAnthropic() {
		t.Error("openai wrongly detected as anthropic")
	}
}

func TestCompleteValidatesModel(t *testing.T) {
	if _, err := Complete(nil, Config{Provider: "openai", APIKey: "k"}, nil, 100); err == nil {
		t.Error("expected error when model is empty")
	}
}
