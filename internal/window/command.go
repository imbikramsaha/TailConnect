package window

// Command returns the argv that starts the window process for the app at
// exe, or false if no native window is available (use a browser instead).
func Command(exe string) ([]string, bool) { return command(exe) }
