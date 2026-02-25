package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Endpoint is the Cloudflare Worker URL that receives telemetry data.
// Replace with the real URL after deploying the Worker.
const Endpoint = "https://worker.stormycloud.org/submit"

// Payload is the anonymous data submitted after a successful generation.
// NEVER include the prefix text, address, keys, or any identifying information.
type Payload struct {
	PrefixLength    int     `json:"prefix_length"`
	DurationSeconds float64 `json:"duration_seconds"`
	CoresUsed       int     `json:"cores_used"`
	Attempts        uint64  `json:"attempts"`
}

// Submit sends the telemetry payload in a background goroutine.
// Errors are silently discarded — telemetry must never affect UX.
func Submit(p Payload) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		body, err := json.Marshal(p)
		if err != nil {
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}()
}
