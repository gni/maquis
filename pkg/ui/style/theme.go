package style

import (
	"image/color"
	"strings"
)

type UITheme struct {
	// Brand & Selection
	Primary   color.Color // Focused titles, active tabs, primary actions
	Secondary color.Color // Auxiliary indicators, breadcrumbs
	Highlight color.Color // Search matches, cursor highlights, badges

	// Surface & Text Hierarchy
	Text       color.Color // Primary readable content
	TextMuted  color.Color // Dimmed content, metadata, hotkey hints
	Background color.Color // Surface background (if controlling canvas)

	// Borders & Dividers
	Border         color.Color // Standard/subdued border (legacy compatibility)
	BorderActive   color.Color // Focused pane border
	BorderInactive color.Color // Unfocused pane borders and column rules

	// Functional Status
	Success color.Color // OK, clean state, added lines
	Warning color.Color // Modifying, uncommitted, cautionary thresholds
	Error   color.Color // Failures, deletions, blocking states

	// Syntax Highlighting Profile
	ChromaStyle string
}

func GetTheme(themeName string) UITheme {
	switch strings.ToLower(themeName) {

	// Modern low-contrast pastel standard (Extremely popular in Neovim/Tmux)
	case "catppuccin", "catppuccin-mocha":
		return UITheme{
			Primary:        Color("#89B4FA"), // Pastel Sky Blue
			Secondary:      Color("#CBA6F7"), // Soft Mauve
			Highlight:      Color("#FAB387"), // Soft Peach
			Text:           Color("#CDD6F4"), // Off-white lavender
			TextMuted:      Color("#6C7086"), // Muted slate gray
			Background:     Color("#1E1E2E"), // Deep charcoal base
			Border:         Color("#313244"), // Subtle surface border
			BorderActive:   Color("#89B4FA"), // Focused cyan-blue
			BorderInactive: Color("#313244"), // Subtle surface border
			Success:        Color("#A6E3A1"), // Muted pastel green
			Warning:        Color("#F9E2AF"), // Soft cream yellow
			Error:          Color("#F38BA8"), // Soft rose red
			ChromaStyle:    "catppuccin-mocha",
		}

	// Deep blue slate with balanced high-legibility pastels
	case "tokyonight", "tokyo-night", "neon":
		return UITheme{
			Primary:        Color("#7AA2F7"), // Slate Blue
			Secondary:      Color("#BB9AF7"), // Muted Purple
			Highlight:      Color("#7DCFFF"), // Pale Cyan
			Text:           Color("#C0CAF5"), // Cool White
			TextMuted:      Color("#565F89"), // Slate Gray
			Background:     Color("#1A1B26"), // Deep Storm Blue
			Border:         Color("#292E42"), // Low-contrast navy
			BorderActive:   Color("#7AA2F7"), // Bright slate blue
			BorderInactive: Color("#292E42"), // Low-contrast navy
			Success:        Color("#9ECE6A"), // Olive-tinted green
			Warning:        Color("#E0AF68"), // Warm ochre
			Error:          Color("#F7768E"), // Muted coral
			ChromaStyle:    "tokyonight-night",
		}

	// Calming natural, earth-toned scheme (Zero eye-strain for dark rooms)
	case "everforest":
		return UITheme{
			Primary:        Color("#83C092"), // Muted Sage Aqua
			Secondary:      Color("#E69875"), // Warm Terracotta
			Highlight:      Color("#DBBC7F"), // Warm Sand
			Text:           Color("#D3C6AA"), // Soft Cream Fg
			TextMuted:      Color("#859289"), // Moss Gray
			Background:     Color("#272E33"), // Deep Olive Charcoal
			Border:         Color("#3D484D"), // Muted Deep Slate
			BorderActive:   Color("#A7C080"), // Soft Grass Green
			BorderInactive: Color("#3D484D"), // Muted Deep Slate
			Success:        Color("#A7C080"), // Grass Green
			Warning:        Color("#DBBC7F"), // Earth Yellow
			Error:          Color("#E67E80"), // Muted Terracotta Red
			ChromaStyle:    "everforest",
		}

	// Classic earthy warm palette
	case "gruvbox":
		return UITheme{
			Primary:        Color("#8EC07C"), // Aqua
			Secondary:      Color("#D3869B"), // Muted Purple
			Highlight:      Color("#FE8019"), // Gruvbox Orange
			Text:           Color("#EBDBB2"), // Warm Sand Parchment
			TextMuted:      Color("#928374"), // Neutral Gray
			Background:     Color("#282828"), // Dark Cocoa Base
			Border:         Color("#504945"), // Dark Gray Brown
			BorderActive:   Color("#FABD2F"), // Warm Yellow
			BorderInactive: Color("#504945"), // Dark Gray Brown
			Success:        Color("#B8BB26"), // Earth Green
			Warning:        Color("#FABD2F"), // Warm Ochre
			Error:          Color("#FB4934"), // Soft Vermilion
			ChromaStyle:    "gruvbox",
		}

	// Clean, anti-glare monochrome without blinding pure-whites
	case "mono", "plain", "minimal":
		return UITheme{
			Primary:        Color("#D4D4D4"), // Crisp medium light gray
			Secondary:      Color("#9E9E9E"), // Balanced mid-tone gray
			Highlight:      Color("#FFFFFF"), // Reserved solely for search matches
			Text:           Color("#E0E0E0"), // 85% off-white (prevents halation)
			TextMuted:      Color("#666666"), // Subdued reading gray
			Background:     Color("#121212"), // OLED/Dark neutral
			Border:         Color("#2E2E2E"), // Receded quiet border
			BorderActive:   Color("#A0A0A0"), // Visible active focus border
			BorderInactive: Color("#2E2E2E"), // Receded quiet border
			Success:        Color("#87AF87"), // Low-saturation sage
			Warning:        Color("#D7AF87"), // Low-saturation sand
			Error:          Color("#D75F5F"), // Low-saturation brick red
			ChromaStyle:    "bw",
		}

	// Light / solarized theme
	case "light":
		return UITheme{
			Primary:        Color("#268BD2"), // Solarized Blue
			Secondary:      Color("#D33682"), // Solarized Magenta
			Highlight:      Color("#B58900"), // Warm Ochre
			Text:           Color("#475B62"), // Solarized Dark Slate
			TextMuted:      Color("#93A1A1"), // Muted Silver Gray
			Background:     Color("#FDF6E3"), // Solarized Base
			Border:         Color("#93A1A1"), // Muted Silver
			BorderActive:   Color("#268BD2"), // Blue Border
			BorderInactive: Color("#EEE8D5"), // Light Inactive Border
			Success:        Color("#859900"), // Warm Green
			Warning:        Color("#B58900"), // Warm Ochre
			Error:          Color("#DC322F"), // Muted Red
			ChromaStyle:    "solarized-light",
		}

	// Japanese mineral pigments (Ultra-low eye fatigue, zero glare)
	case "kanagawa", "kanagawa-wave":
		return UITheme{
			Primary:        Color("#7E9CD8"), // Crystal Blue (Muted, anti-glare)
			Secondary:      Color("#957FB8"), // Spring Violet (Low-saturation iris)
			Highlight:      Color("#DCA561"), // Autumn Ochre (Warm desaturated sand)
			Text:           Color("#DCD7BA"), // Fuji White (Warm parchment, zero eye strain)
			TextMuted:      Color("#727169"), // Sumi Gray (Balanced readable neutral)
			Background:     Color("#1F1F28"), // Sumi Ink (Deep matte charcoal)
			Border:         Color("#2A2A37"), // Subdued ink divider
			BorderActive:   Color("#7E9CD8"), // Focused crystal blue
			BorderInactive: Color("#2A2A37"), // Subdued ink divider
			Success:        Color("#76946A"), // Forest Moss (Calm organic green)
			Warning:        Color("#E6C384"), // Pale Ochre (Soft natural caution)
			Error:          Color("#C34043"), // Lacquer Red (Deep brick red, not neon)
			ChromaStyle:    "dracula",
		}

	// Nocturnal foam & desaturated mauve (Zero retina burn)
	case "rose-pine", "rose-pine-moon", "rosepine":
		return UITheme{
			Primary:        Color("#9CCFD8"), // Foam Aqua (Calm sea foam)
			Secondary:      Color("#C4A7E7"), // Iris Mauve (Gentle desaturated lilac)
			Highlight:      Color("#F6C177"), // Desert Gold (Warm honey sand)
			Text:           Color("#E0DEF4"), // Soft White (Gentle lavender-tinted foreground)
			TextMuted:      Color("#6E6A86"), // Muted Slate (Low-contrast background text)
			Background:     Color("#232136"), // Dark Plum Navy (Restful midnight canvas)
			Border:         Color("#393552"), // Subtle plum border
			BorderActive:   Color("#9CCFD8"), // Focused foam border
			BorderInactive: Color("#393552"), // Subtle plum border
			Success:        Color("#3E8FB0"), // Pine Cyan (Cool oceanic green-blue)
			Warning:        Color("#F6C177"), // Desert Gold (Warm honey caution)
			Error:          Color("#EB6F92"), // Muted Berry (Soft rose, no retina burn)
			ChromaStyle:    "dracula",
		}

	// Natural earth & paper tone (Zero blue-light fatigue, warm incandescence)
	case "zenburn", "earth-calm", "earth":
		return UITheme{
			Primary:        Color("#8CD0D3"), // Sea Green (Low-contrast aqua)
			Secondary:      Color("#DC8CC3"), // Muted Plum (Subdued lavender)
			Highlight:      Color("#DFAF8F"), // Peach Tan (Warm earthen accent)
			Text:           Color("#DCDCCC"), // Bleached Parchment (Zero glare paper tone)
			TextMuted:      Color("#7F9F7F"), // Lichen Sage (Soft green-gray secondary)
			Background:     Color("#2B2B2B"), // Warm Charcoal (No pure black eye strain)
			Border:         Color("#3F3F3F"), // Subdued charcoal border
			BorderActive:   Color("#8CD0D3"), // Focused aqua border
			BorderInactive: Color("#3F3F3F"), // Subdued charcoal border
			Success:        Color("#7F9F7F"), // Lichen Green (Restful organic green)
			Warning:        Color("#DFAF8F"), // Peach Tan (Low-saturation warning)
			Error:          Color("#CC9393"), // Muted Brick (Dusty terracotta red)
			ChromaStyle:    "friendly",
		}

	// Arctic, clean, blue-gray tone
	case "dark", "nord", "nord-calm":
		fallthrough
	default:
		return UITheme{
			Primary:        Color("#88C0D0"), // Frost Cyan
			Secondary:      Color("#81A1C1"), // Glacier Slate (Balanced auxiliary tone)
			Highlight:      Color("#8FBCBB"), // Soft Sea Green
			Text:           Color("#D8DEE9"), // Snow Mist (Subdued gray-white, prevents halation)
			TextMuted:      Color("#616E88"), // Polar Twilight Gray
			Background:     Color("#2E3440"), // Deep Polar Blue
			Border:         Color("#3B4252"), // Subdued Dark Polar
			BorderActive:   Color("#88C0D0"), // Bright Ice Blue
			BorderInactive: Color("#3B4252"), // Subdued Dark Polar
			Success:        Color("#A3BE8C"), // Sage Green
			Warning:        Color("#EBCB8B"), // Soft Ochre
			Error:          Color("#BF616A"), // Rust Red
			ChromaStyle:    "nord",
		}
	}
}
