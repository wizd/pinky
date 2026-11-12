package vcorplink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 20 * time.Second}

func post(ctx context.Context, url string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

// Claim asks which account a pairing code belongs to, so the person can
// check it before Pinky pairs. It uses the code up only on Confirm.
func Claim(ctx context.Context, origin, code string) (account string, err error) {
	var r struct {
		Account string `json:"account"`
	}
	if err := post(ctx, origin+"/api/ext/pair/claim", map[string]string{"code": code}, &r); err != nil {
		return "", err
	}
	return r.Account, nil
}

// Pair makes a key, confirms the code with its public half and returns the
// config to save.
func Pair(ctx context.Context, origin, code, name string) (*Config, error) {
	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	jwk, err := PublicJWK(key)
	if err != nil {
		return nil, err
	}
	var r struct {
		DeviceID string `json:"deviceId"`
		Account  string `json:"account"`
	}
	body := map[string]any{"code": code, "publicKey": jwk, "name": name, "kind": "pinky"}
	if err := post(ctx, origin+"/api/ext/pair/confirm", body, &r); err != nil {
		return nil, err
	}
	if r.DeviceID == "" {
		return nil, fmt.Errorf("the platform gave no device id")
	}
	return &Config{Origin: origin, DeviceID: r.DeviceID, Account: r.Account, Key: key, Name: name}, nil
}
