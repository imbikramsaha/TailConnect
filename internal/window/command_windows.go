package window

func command(exe string) ([]string, bool) {
	if !supported {
		return nil, false
	}
	return []string{exe, "window"}, true
}
