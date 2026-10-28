package update

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

const DefaultMetricsURL = "https://infinitelogic.app/o/hsh-home/hooks/metrics_report"

const metricsTimeout = 10 * time.Second

type report struct {
	Product                 string `json:"product"`
	Version                 string `json:"version"`
	Platform                string `json:"platform"`
	SessionsSinceLastUpdate int    `json:"sessions_since_last_update"`
}

func Report(url, version string, sessions int) error {
	body, err := json.Marshal(report{
		Product:                 "hsh",
		Version:                 version,
		Platform:                runtime.GOOS + "/" + runtime.GOARCH,
		SessionsSinceLastUpdate: sessions,
	})
	if err != nil {
		return err
	}
	if url == "" {
		url = DefaultMetricsURL
	}
	client := &http.Client{Timeout: metricsTimeout}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("report usage metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("report usage metrics: unexpected status %s", resp.Status)
	}
	return nil
}
