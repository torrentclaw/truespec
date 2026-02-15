package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	vtBaseURL      = "https://www.virustotal.com/api/v3"
	vtWebURL       = "https://www.virustotal.com/gui/file"
	vtMaxUploadMB  = 20
	vtMaxUploadB   = vtMaxUploadMB * 1024 * 1024
	vtPollInterval = 15 * time.Second
	vtPollTimeout  = 3 * time.Minute
	vtRateInterval = 15 * time.Second // 4 requests per minute = 1 per 15s
)

// VTClient is a VirusTotal API v3 client with rate limiting.
type VTClient struct {
	apiKey     string
	httpClient *http.Client
	mu         sync.Mutex
	lastReq    time.Time
}

// VTFileReport is the parsed response from a VT file lookup or analysis.
type VTFileReport struct {
	Detected     bool     `json:"detected"`       // any engine detected it
	Detections   int      `json:"detections"`     // number of engines that flagged it
	TotalEngines int      `json:"total_engines"`  // total engines that scanned
	MalwareNames []string `json:"malware_names"`  // names from engines that detected
	Permalink    string   `json:"permalink"`      // link to VT web report
	ScanDate     string   `json:"scan_date"`      // when the scan was performed
	Status       string   `json:"status"`         // vt_clean, vt_malware, vt_unknown, vt_error
	UploadedByUs bool     `json:"uploaded_by_us"` // true if we uploaded the file
}

// vtAPIResponse matches the VT v3 API response structure.
type vtAPIResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			LastAnalysisDate    int64                       `json:"last_analysis_date"`
			LastAnalysisStats   vtAnalysisStats             `json:"last_analysis_stats"`
			LastAnalysisResults map[string]vtAnalysisResult `json:"last_analysis_results"`
		} `json:"attributes"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type vtAnalysisStats struct {
	Harmless         int `json:"harmless"`
	Malicious        int `json:"malicious"`
	Suspicious       int `json:"suspicious"`
	Undetected       int `json:"undetected"`
	Timeout          int `json:"timeout"`
	ConfirmedTimeout int `json:"confirmed-timeout"`
	Failure          int `json:"failure"`
	TypeUnsupported  int `json:"type-unsupported"`
}

type vtAnalysisResult struct {
	Category   string `json:"category"` // malicious, suspicious, harmless, undetected
	EngineName string `json:"engine_name"`
	Result     string `json:"result"` // malware name or null
}

// vtAnalysisResponse matches the response from POST /files (upload) endpoint.
type vtAnalysisResponse struct {
	Data struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// NewVTClient creates a new VirusTotal client with the given API key.
func NewVTClient(apiKey string) *VTClient {
	return &VTClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// rateLimit waits to ensure we don't exceed 4 requests per minute.
func (c *VTClient) rateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	elapsed := time.Since(c.lastReq)
	if elapsed < vtRateInterval {
		time.Sleep(vtRateInterval - elapsed)
	}
	c.lastReq = time.Now()
}

// LookupHash queries VT for a file by its SHA256 hash.
// Returns nil report with nil error if the file is not found (404).
func (c *VTClient) LookupHash(ctx context.Context, sha256 string) (*VTFileReport, error) {
	c.rateLimit()

	url := fmt.Sprintf("%s/files/%s", vtBaseURL, sha256)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-apikey", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("VT API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil // file not in VT database
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024)) // 1MB max
	if err != nil {
		return nil, fmt.Errorf("read VT response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("VT API error %d: %s", resp.StatusCode, string(body))
	}

	var apiResp vtAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse VT response: %w", err)
	}

	return parseFileReport(apiResp, sha256, false), nil
}

// UploadFile uploads a file to VT for scanning. File must be ≤ 20MB.
// Returns the analysis ID for polling, or an error.
func (c *VTClient) UploadFile(ctx context.Context, filePath string) (string, error) {
	// Check file size
	info, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.Size() > vtMaxUploadB {
		return "", fmt.Errorf("file too large for upload: %d bytes (max %dMB)", info.Size(), vtMaxUploadMB)
	}

	c.rateLimit()

	// Create multipart form
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer writer.Close()

		part, err := writer.CreateFormFile("file", filepath.Base(filePath))
		if err != nil {
			pw.CloseWithError(err)
			return
		}

		f, err := os.Open(filePath)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		defer f.Close()

		if _, err := io.Copy(part, f); err != nil {
			pw.CloseWithError(err)
			return
		}
	}()

	url := fmt.Sprintf("%s/files", vtBaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, pr)
	if err != nil {
		return "", fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("x-apikey", c.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Use a longer timeout for uploads
	uploadClient := &http.Client{Timeout: 2 * time.Minute}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("VT upload: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", fmt.Errorf("read upload response: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("VT upload error %d: %s", resp.StatusCode, string(body))
	}

	var analysisResp vtAnalysisResponse
	if err := json.Unmarshal(body, &analysisResp); err != nil {
		return "", fmt.Errorf("parse upload response: %w", err)
	}

	if analysisResp.Data.ID == "" {
		return "", fmt.Errorf("VT upload returned empty analysis ID")
	}

	return analysisResp.Data.ID, nil
}

// PollAnalysis waits for a VT analysis to complete and returns the report.
func (c *VTClient) PollAnalysis(ctx context.Context, analysisID string) (*VTFileReport, error) {
	deadline := time.After(vtPollTimeout)
	ticker := time.NewTicker(vtPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("VT analysis timed out after %s", vtPollTimeout)
		case <-ticker.C:
			c.rateLimit()

			url := fmt.Sprintf("%s/analyses/%s", vtBaseURL, analysisID)
			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				return nil, fmt.Errorf("create poll request: %w", err)
			}
			req.Header.Set("x-apikey", c.apiKey)

			resp, err := c.httpClient.Do(req)
			if err != nil {
				continue // retry on transient errors
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			resp.Body.Close()
			if err != nil {
				continue
			}

			if resp.StatusCode != 200 {
				continue
			}

			var result struct {
				Data struct {
					Attributes struct {
						Status  string                      `json:"status"`
						Stats   vtAnalysisStats             `json:"stats"`
						Results map[string]vtAnalysisResult `json:"results"`
					} `json:"attributes"`
				} `json:"data"`
			}

			if err := json.Unmarshal(body, &result); err != nil {
				continue
			}

			if result.Data.Attributes.Status == "completed" {
				return parseAnalysisReport(result.Data.Attributes.Stats, result.Data.Attributes.Results, true), nil
			}
			// Still queued/running — keep polling
		}
	}
}

// parseFileReport converts a VT API file response to our VTFileReport.
func parseFileReport(apiResp vtAPIResponse, sha256 string, uploadedByUs bool) *VTFileReport {
	stats := apiResp.Data.Attributes.LastAnalysisStats
	malicious := stats.Malicious + stats.Suspicious
	total := stats.Harmless + stats.Malicious + stats.Suspicious + stats.Undetected

	var malwareNames []string
	for _, r := range apiResp.Data.Attributes.LastAnalysisResults {
		if r.Category == "malicious" || r.Category == "suspicious" {
			if r.Result != "" {
				malwareNames = append(malwareNames, r.Result)
			}
		}
	}

	// Deduplicate malware names (many engines report same name)
	malwareNames = dedup(malwareNames)
	if len(malwareNames) > 10 {
		malwareNames = malwareNames[:10] // cap to top 10
	}

	status := "vt_clean"
	if malicious > 0 {
		status = "vt_malware"
	}

	scanDate := ""
	if apiResp.Data.Attributes.LastAnalysisDate > 0 {
		scanDate = time.Unix(apiResp.Data.Attributes.LastAnalysisDate, 0).UTC().Format(time.RFC3339)
	}

	return &VTFileReport{
		Detected:     malicious > 0,
		Detections:   malicious,
		TotalEngines: total,
		MalwareNames: malwareNames,
		Permalink:    fmt.Sprintf("%s/%s", vtWebURL, sha256),
		ScanDate:     scanDate,
		Status:       status,
		UploadedByUs: uploadedByUs,
	}
}

// parseAnalysisReport converts a VT analysis response to our VTFileReport.
func parseAnalysisReport(stats vtAnalysisStats, results map[string]vtAnalysisResult, uploadedByUs bool) *VTFileReport {
	malicious := stats.Malicious + stats.Suspicious
	total := stats.Harmless + stats.Malicious + stats.Suspicious + stats.Undetected

	var malwareNames []string
	for _, r := range results {
		if r.Category == "malicious" || r.Category == "suspicious" {
			if r.Result != "" {
				malwareNames = append(malwareNames, r.Result)
			}
		}
	}

	malwareNames = dedup(malwareNames)
	if len(malwareNames) > 10 {
		malwareNames = malwareNames[:10]
	}

	status := "vt_clean"
	if malicious > 0 {
		status = "vt_malware"
	}

	return &VTFileReport{
		Detected:     malicious > 0,
		Detections:   malicious,
		TotalEngines: total,
		MalwareNames: malwareNames,
		ScanDate:     time.Now().UTC().Format(time.RFC3339),
		Status:       status,
		UploadedByUs: uploadedByUs,
	}
}

// dedup removes duplicate strings preserving order.
func dedup(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
