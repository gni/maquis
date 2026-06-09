package ui

import (
	"image/color"
	"strings"

	"bidouille/pkg/ui/style"
)

type UITheme struct {
	Primary   color.Color
	Secondary color.Color
	Text      color.Color
	Border    color.Color
	Success   color.Color
	Error     color.Color
	Highlight color.Color
}

func GetTheme(themeName string) UITheme {
	switch strings.ToLower(themeName) {
	case "neon":
		// Soft Cyberpunk / Dracula Hybrid (Muted pastel neon)
		return UITheme{
			Primary:   style.Color("#8BE9FD"), // Soft Cyan
			Secondary: style.Color("#FF79C6"), // Soft Pink/Magenta
			Text:      style.Color("#F8F8F2"), // Soft Off-White
			Border:    style.Color("#6272A4"), // Muted Blue-Gray
			Success:   style.Color("#50FA7B"), // Soft Green
			Error:     style.Color("#FF5555"), // Soft Red
			Highlight: style.Color("#F1FA8C"), // Soft Yellow
		}
	case "light":
		// Solarized Light
		return UITheme{
			Primary:   style.Color("#268BD2"), // Solarized Blue
			Secondary: style.Color("#D33682"), // Solarized Magenta
			Text:      style.Color("#475B62"), // Solarized Dark Slate
			Border:    style.Color("#93A1A1"), // Muted Silver
			Success:   style.Color("#859900"), // Warm Green
			Error:     style.Color("#DC322F"), // Muted Red
			Highlight: style.Color("#B58900"), // Warm Ochre
		}
	case "gruvbox":
		// Warm retro/earthy theme
		return UITheme{
			Primary:   style.Color("#8EC07C"), // Soft Aqua
			Secondary: style.Color("#D3869B"), // Soft Pink
			Text:      style.Color("#EBDBB2"), // Warm Sand
			Border:    style.Color("#665C54"), // Muted Dark Gray
			Success:   style.Color("#B8BB26"), // Warm Green
			Error:     style.Color("#FB4934"), // Warm Red
			Highlight: style.Color("#FABD2F"), // Warm Yellow
		}
	case "dark":
		fallthrough
	default:
		// Nord Dark (Very relaxing, muted arctic style)
		return UITheme{
			Primary:   style.Color("#88C0D0"), // Nord Frost Blue
			Secondary: style.Color("#B48EAD"), // Nord Lavender
			Text:      style.Color("#E5E9F0"), // Nord Snow
			Border:    style.Color("#4C566A"), // Nord Muted Slate Gray
			Success:   style.Color("#A3BE8C"), // Nord Sage Green
			Error:     style.Color("#BF616A"), // Nord Rust Red
			Highlight: style.Color("#EBCB8B"), // Nord Yellow/Ochre
		}
	}
}
