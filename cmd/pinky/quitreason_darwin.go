//go:build darwin

package main

import "github.com/ebitengine/purego/objc"

// Apple event codes of a quit and why it was sent (AERegistry.h).
const (
	aeQuitApplication = 'q'<<24 | 'u'<<16 | 'i'<<8 | 't'
	aeQuitReason      = 'w'<<24 | 'h'<<16 | 'y'<<8 | '?'
)

// The reasons loginwindow gives when the session ends.
var sessionEndReasons = map[uint32]bool{
	'l'<<24 | 'o'<<16 | 'g'<<8 | 'o': true, // kAELogOut
	'r'<<24 | 'l'<<16 | 'g'<<8 | 'o': true, // kAEReallyLogOut
	'r'<<24 | 'r'<<16 | 's'<<8 | 't': true, // kAEShowRestartDialog
	'r'<<24 | 's'<<16 | 'd'<<8 | 'n': true, // kAEShowShutdownDialog
	'r'<<24 | 'e'<<16 | 's'<<8 | 't': true, // kAERestart
	's'<<24 | 'h'<<16 | 'u'<<8 | 't': true, // kAEShutDown
}

// sessionEnding reports whether the quit that starts comes from the
// system ending the session. On macOS loginwindow asks each app to quit
// with a quit Apple event, as the Dock's Quit does, before MyGo hears that
// the session ends; the event says why. Call it on the main thread from a
// quit listener, while the event is handled.
func sessionEnding() bool {
	aem := objc.ID(objc.GetClass("NSAppleEventManager")).Send(objc.RegisterName("sharedAppleEventManager"))
	if aem == 0 {
		return false
	}
	ev := aem.Send(objc.RegisterName("currentAppleEvent"))
	if ev == 0 || objc.Send[uint32](ev, objc.RegisterName("eventID")) != aeQuitApplication {
		return false // Cmd-Q, a Quit menu item, `pinky quit`
	}
	why := ev.Send(objc.RegisterName("attributeDescriptorForKeyword:"), uintptr(aeQuitReason))
	if why == 0 {
		return false // the Dock's Quit, or a script
	}
	reason := objc.Send[uint32](why, objc.RegisterName("typeCodeValue"))
	if reason == 0 {
		reason = objc.Send[uint32](why, objc.RegisterName("enumCodeValue"))
	}
	return sessionEndReasons[reason]
}
