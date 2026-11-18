//go:build !darwin

package main

// sessionEnding reports whether the quit that starts comes from the system
// ending the session. Windows says so in the quit event itself
// (mygo.QuitEvent.SessionEnding), and on Linux the session's end is a
// signal (quitIntent.signalled).
func sessionEnding() bool { return false }
