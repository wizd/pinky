package ios

import "context"

// Services lists the services the device offers on its tunnel, for
// diagnostics; none on a device without one.
func (d *Device) Services() []string {
	var out []string
	for name := range d.cur().rsd.Services {
		out = append(out, name)
	}
	return out
}

// LaunchID launches an app by its bundle id, without looking it up in the
// app catalog.
func (d *Device) LaunchID(ctx context.Context, bundleID string) error {
	return d.gone(d.startApp(ctx, bundleID, ""))
}

// Done is closed once the tunnel the device was opened over is gone; the
// caller then opens it again. It is never closed on a device without one.
func (d *Device) Done() <-chan struct{} {
	l := d.cur()
	if l.tunnel == nil {
		return nil
	}
	return l.tunnel.Done()
}
