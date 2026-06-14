package ui

import (
	"bidouille/pkg/config"
	"bidouille/pkg/ui/style"
)

type UITheme = style.UITheme

func GetTheme(themeName string) UITheme {
	return style.GetTheme(themeName)
}

func GetConfiguredTheme(cfg *config.Config) UITheme {
	theme := style.GetTheme(cfg.Theme)
	if cfg.SyntaxTheme != "" && cfg.SyntaxTheme != "auto" {
		theme.ChromaStyle = cfg.SyntaxTheme
	}
	return theme
}
