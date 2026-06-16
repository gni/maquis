package ui

import (
	"maquis/pkg/config"
	"maquis/pkg/ui/style"
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
