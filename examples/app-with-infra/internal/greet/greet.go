// Package greet builds the response body.
package greet

import "fmt"

// Message returns the greeting served at /.
func Message(env string) string {
	return fmt.Sprintf("hello from %s\n", env)
}
