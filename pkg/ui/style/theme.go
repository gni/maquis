package style

import (
	"image/color"
	"strings"
)

type UITheme struct {
	Primary     color.Color
	Secondary   color.Color
	Text        color.Color
	Border      color.Color
	Success     color.Color
	Error       color.Color
	Highlight   color.Color
	ChromaStyle string
}

func GetTheme(themeName string) UITheme {
	switch strings.ToLower(themeName) {
	case "neon":
		return UITheme{
			Primary:     Color("#8BE9FD"), // Soft Cyan
			Secondary:   Color("#FF79C6"), // Soft Pink/Magenta
			Text:        Color("#F8F8F2"), // Soft Off-White
			Border:      Color("#6272A4"), // Muted Blue-Gray
			Success:     Color("#50FA7B"), // Soft Green
			Error:       Color("#FF5555"), // Soft Red
			Highlight:   Color("#F1FA8C"), // Soft Yellow
			ChromaStyle: "dracula",
		}
	case "light":
		return UITheme{
			Primary:     Color("#268BD2"), // Solarized Blue
			Secondary:   Color("#D33682"), // Solarized Magenta
			Text:        Color("#475B62"), // Solarized Dark Slate
			Border:      Color("#93A1A1"), // Muted Silver
			Success:     Color("#859900"), // Warm Green
			Error:       Color("#DC322F"), // Muted Red
			Highlight:   Color("#B58900"), // Warm Ochre
			ChromaStyle: "solarized-light",
		}
	case "gruvbox":
		return UITheme{
			Primary:     Color("#8EC07C"), // Soft Aqua
			Secondary:   Color("#D3869B"), // Soft Pink
			Text:        Color("#EBDBB2"), // Warm Sand
			Border:      Color("#665C54"), // Muted Dark Gray
			Success:     Color("#B8BB26"), // Warm Green
			Error:       Color("#FB4934"), // Warm Red
			Highlight:   Color("#FABD2F"), // Warm Yellow
			ChromaStyle: "gruvbox",
		}
	case "plain":
		return UITheme{
			Primary:     Color("#ffffff"), // Pure White
			Secondary:   Color("#ffffff"), // Pure White
			Text:        Color("#ffffff"), // Pure White
			Border:      Color("#555555"), // Muted Gray
			Success:     Color("#10b981"), // Green for success/done symbols
			Error:       Color("#ef4444"), // Red for error symbols
			Highlight:   Color("#ffffff"), // Pure White
			ChromaStyle: "bw",
		}
	case "mono":
		return UITheme{
			Primary:     Color("#b2b2b2"), // Soft light gray
			Secondary:   Color("#777777"), // Softer medium-dark gray
			Text:        Color("#cccccc"), // Soft off-white (comfortable to read)
			Border:      Color("#3a3a3a"), // Deep charcoal border (low contrast, clean)
			Success:     Color("#87af87"), // Soothing sage green
			Error:       Color("#d75f5f"), // Soothing muted red
			Highlight:   Color("#e5e5e5"), // Crisp light gray for key details
			ChromaStyle: "bw",
		}
	case "minimal":
		return UITheme{
			Primary:     Color("#d2c4b4"), // Soft warm tan/sand
			Secondary:   Color("#8b7e74"), // Muted warm gray
			Text:        Color("#e8dfd8"), // Soothing warm cream/beige (anti-blue-light)
			Border:      Color("#38322e"), // Deep warm charcoal border
			Success:     Color("#87af87"), // Soothing sage green
			Error:       Color("#d75f5f"), // Soothing muted red
			Highlight:   Color("#f4efeb"), // Softer warm white for key details
			ChromaStyle: "bw",
		}
	case "dark":
		fallthrough
	default:
		return UITheme{
			Primary:     Color("#88C0D0"), // Nord Frost Blue
			Secondary:   Color("#B48EAD"), // Nord Lavender
			Text:        Color("#E5E9F0"), // Nord Snow
			Border:      Color("#4C566A"), // Nord Muted Slate Gray
			Success:     Color("#A3BE8C"), // Nord Sage Green
			Error:       Color("#BF616A"), // Nord Rust Red
			Highlight:   Color("#EBCB8B"), // Nord Yellow/Ochre
			ChromaStyle: "nord",
		}
	}
}
