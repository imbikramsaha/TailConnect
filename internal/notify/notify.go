// Package notify shows system notifications. Clicking one calls the
// handler set with OnClick with the notification's route (macOS).
package notify

import "sync"

var (
	mu      sync.Mutex
	onClick func(route string)
)

// OnClick sets the handler for notification clicks.
func OnClick(fn func(route string)) {
	mu.Lock()
	onClick = fn
	mu.Unlock()
}

func clicked(route string) {
	mu.Lock()
	fn := onClick
	mu.Unlock()
	if fn != nil {
		go fn(route)
	}
}

// Send shows a notification; route is where a click should take the user
// (e.g. "#/device/p-123"). Errors are not fatal and are returned for logging.
func Send(title, body, route string) error { return send(title, body, route) }
