package agentmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func RegisterPairingChallenge(ctx context.Context, bridgeURL, challenge, origin string) error {
	body, err := json.Marshal(map[string]string{"challenge": challenge, "origin": origin})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, bridgeURL+"/v1/internal/pairing-challenge", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Timewarp-Protocol-Handler", "1")
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("existing bridge rejected pairing challenge with HTTP %d", response.StatusCode)
	}
	return nil
}
