package ui

import (
	"bidouille/pkg/ui/style"
)

type UITheme = style.UITheme

func GetTheme(themeName string) UITheme {
	return style.GetTheme(themeName)
}
