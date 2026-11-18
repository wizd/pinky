package history

import (
	"context"
	"time"
)

// SetDeviceTraffic saves whether agents may read a phone's network traffic
// (docs/notes/traffic-capture.md). Off removes the phone's row, and with
// it the route switch (SetDeviceTrafficRoute).
func (s *Store) SetDeviceTraffic(ctx context.Context, deviceID string, on bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !on {
		_, err := s.db.ExecContext(ctx, `DELETE FROM traffic_devices WHERE device_id = ?`, deviceID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO traffic_devices (device_id, on_at) VALUES (?, ?)
		ON CONFLICT(device_id) DO NOTHING`, deviceID, ms(time.Now()))
	return err
}

// SetDeviceTrafficRoute saves whether Pinky points an Android phone's HTTP
// proxy at itself during tasks. It applies to phones whose traffic agents
// may read only.
func (s *Store) SetDeviceTrafficRoute(ctx context.Context, deviceID string, on bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE traffic_devices SET route = ? WHERE device_id = ?`, v, deviceID)
	return err
}

// TrafficDevices returns the phones whose network traffic agents may
// read, and of those the ones Pinky routes through itself during tasks.
func (s *Store) TrafficDevices(ctx context.Context) (on, route map[string]bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, route FROM traffic_devices`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	on, route = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var id string
		var r int
		if err := rows.Scan(&id, &r); err != nil {
			return nil, nil, err
		}
		on[id] = true
		if r != 0 {
			route[id] = true
		}
	}
	return on, route, rows.Err()
}

// TrafficRoute is a phone whose HTTP proxy Pinky set, and what to put
// back.
type TrafficRoute struct {
	DeviceID string
	Prev     string // the setting before; "" for none
	Port     int
}

// SaveTrafficRoute notes, before Pinky sets a phone's HTTP proxy, what to
// put back, so that a Pinky that crashed puts it back at its next start.
func (s *Store) SaveTrafficRoute(ctx context.Context, r TrafficRoute) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO traffic_routes (device_id, prev, port, routed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET prev = excluded.prev, port = excluded.port, routed_at = excluded.routed_at`,
		r.DeviceID, r.Prev, r.Port, ms(time.Now()))
	return err
}

// DeleteTrafficRoute forgets a route once its setting was put back.
func (s *Store) DeleteTrafficRoute(ctx context.Context, deviceID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM traffic_routes WHERE device_id = ?`, deviceID)
	return err
}

// TrafficRoutes returns the routes whose settings are still to be put back.
func (s *Store) TrafficRoutes(ctx context.Context) ([]TrafficRoute, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, prev, port FROM traffic_routes ORDER BY device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrafficRoute
	for rows.Next() {
		var r TrafficRoute
		if err := rows.Scan(&r.DeviceID, &r.Prev, &r.Port); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetTrafficBlocklist saves the hosts Pinky never decrypts or records for a
// phone, on top of its built-in list; none removes them. The list stays
// when reading the phone's traffic is turned off.
func (s *Store) SetTrafficBlocklist(ctx context.Context, deviceID string, hosts []string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM traffic_blocklist WHERE device_id = ?`, deviceID); err != nil {
		return err
	}
	for _, h := range hosts {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO traffic_blocklist (device_id, host) VALUES (?, ?)`, deviceID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TrafficBlocklists returns the hosts the person blocked, by phone, in the
// order of their names.
func (s *Store) TrafficBlocklists(ctx context.Context) (map[string][]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, host FROM traffic_blocklist ORDER BY device_id, host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = append(out[id], h)
	}
	return out, rows.Err()
}
