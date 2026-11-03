package cli

import "strings"

var terminalCapabilityVars = []string{
	"TERM_PROGRAM",
	"TERM_PROGRAM_VERSION",
	"COLORTERM",
	"VTE_VERSION",
	"WT_SESSION",
	"LC_TERMINAL",
	"LC_TERMINAL_VERSION",
	"KONSOLE_VERSION",
	"DOMTERM",
	"FORCE_HYPERLINK",
}

var hyperlinkTerms = []string{"ghostty", "kitty", "alacritty", "wezterm", "foot", "contour"}

func terminalEnvArgs(getenv func(string) string) []string {
	var args []string
	for _, name := range terminalCapabilityVars {
		if v := getenv(name); v != "" {
			args = append(args, "-e", name+"="+v)
		}
	}
	if getenv("FORCE_HYPERLINK") == "" {
		term := strings.ToLower(getenv("TERM"))
		for _, t := range hyperlinkTerms {
			if strings.Contains(term, t) {
				args = append(args, "-e", "FORCE_HYPERLINK=1")
				break
			}
		}
	}
	return args
}
