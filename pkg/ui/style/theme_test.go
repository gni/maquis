package style

import (
	"testing"
)

func TestGetThemeAllThemesPopulated(t *testing.T) {
	themeNames := []string{
		"catppuccin",
		"catppuccin-mocha",
		"tokyonight",
		"tokyo-night",
		"everforest",
		"gruvbox",
		"mono",
		"nord",
		"dark",
		"light",
	}

	for _, name := range themeNames {
		t.Run(name, func(t *testing.T) {
			theme := GetTheme(name)
			if theme.Primary == nil {
				t.Errorf("%s: Primary is nil", name)
			}
			if theme.Secondary == nil {
				t.Errorf("%s: Secondary is nil", name)
			}
			if theme.Highlight == nil {
				t.Errorf("%s: Highlight is nil", name)
			}
			if theme.Text == nil {
				t.Errorf("%s: Text is nil", name)
			}
			if theme.TextMuted == nil {
				t.Errorf("%s: TextMuted is nil", name)
			}
			if theme.Border == nil {
				t.Errorf("%s: Border (legacy) is nil", name)
			}
			if theme.BorderActive == nil {
				t.Errorf("%s: BorderActive is nil", name)
			}
			if theme.BorderInactive == nil {
				t.Errorf("%s: BorderInactive is nil", name)
			}
			if theme.Success == nil {
				t.Errorf("%s: Success is nil", name)
			}
			if theme.Warning == nil {
				t.Errorf("%s: Warning is nil", name)
			}
			if theme.Error == nil {
				t.Errorf("%s: Error is nil", name)
			}
			if theme.ChromaStyle == "" {
				t.Errorf("%s: ChromaStyle is empty", name)
			}
		})
	}
}
