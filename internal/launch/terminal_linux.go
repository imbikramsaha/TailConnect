package launch

import "fmt"

type termSpec struct {
	bin  string
	args func(cmd []string) []string
}

func dashDash(cmd []string) []string { return append([]string{"--"}, cmd...) }
func dashE(cmd []string) []string    { return append([]string{"-e"}, cmd...) }

var linuxTerminals = []termSpec{
	{"ptyxis", func(c []string) []string { return append([]string{"--new-window", "--"}, c...) }},
	{"gnome-terminal", dashDash},
	{"konsole", dashE},
	{"xfce4-terminal", func(c []string) []string { return append([]string{"-x"}, c...) }},
	{"tilix", func(c []string) []string { return []string{"-e", shellJoin(c)} }},
	{"kitty", func(c []string) []string { return c }},
	{"alacritty", dashE},
	{"wezterm", func(c []string) []string { return append([]string{"start", "--"}, c...) }},
	{"foot", func(c []string) []string { return c }},
	{"xterm", dashE},
	{"x-terminal-emulator", dashE},
}

// Terminals lists terminals found on this machine.
func Terminals() []string {
	var out []string
	for _, t := range linuxTerminals {
		if _, err := LookPath(t.bin); err == nil {
			out = append(out, t.bin)
		}
	}
	return out
}

// Terminal opens a new terminal window running argv.
func Terminal(preferred string, argv []string) error {
	order := linuxTerminals
	if preferred != "" {
		for _, t := range linuxTerminals {
			if t.bin == preferred {
				order = append([]termSpec{t}, linuxTerminals...)
				break
			}
		}
	}
	for _, t := range order {
		bin, err := LookPath(t.bin)
		if err != nil {
			continue
		}
		return start(bin, t.args(argv)...)
	}
	return fmt.Errorf("no terminal emulator found; set one in Settings")
}
