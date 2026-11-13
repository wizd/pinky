package adb

import "errors"

// serverProcesses: on Windows Pinky tells no server its own (there is no
// Local Network privacy to lose there), so it stops none.
func serverProcesses(port string) ([]process, error) { return nil, errors.ErrUnsupported }

// processList: Windows has no ps, and Pinky looks for no other adb
// servers there (OtherServers).
func processList() (string, error) { return "", errors.ErrUnsupported }

func stopProcess(pid int) error { return errors.ErrUnsupported }
