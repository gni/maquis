package config

import _ "embed"

//go:embed default_prompt.md
var DefaultSystemInstruction string
