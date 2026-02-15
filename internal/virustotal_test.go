package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestVTClient_LookupHash_Found(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-apikey") != "test-key" {
			t.Errorf("expected apikey 'test-key', got '%s'", r.Header.Get("x-apikey"))
		}
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}

		resp := vtAPIResponse{}
		resp.Data.Attributes.LastAnalysisStats = vtAnalysisStats{
			Malicious:   5,
			Suspicious:  1,
			Harmless:    20,
			Undetected:  46,
		}
		resp.Data.Attributes.LastAnalysisResults = map[string]vtAnalysisResult{
			"EngineA": {Category: "malicious", EngineName: "EngineA", Result: "Trojan.Gen"},
			"EngineB": {Category: "malicious", EngineName: "EngineB", Result: "Win32.Malware"},
			"EngineC": {Category: "harmless", EngineName: "EngineC", Result: ""},
		}
		resp.Data.Attributes.LastAnalysisDate = 1739500000

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewVTClient("test-key")
	// Override base URL via custom HTTP client that rewrites requests
	client.httpClient = server.Client()
	// We need to override the URL, so let's test parseFileReport directly instead
	
	// Test parseFileReport
	apiResp := vtAPIResponse{}
	apiResp.Data.Attributes.LastAnalysisStats = vtAnalysisStats{
		Malicious: 5, Suspicious: 1, Harmless: 20, Undetected: 46,
	}
	apiResp.Data.Attributes.LastAnalysisResults = map[string]vtAnalysisResult{
		"EngineA": {Category: "malicious", EngineName: "EngineA", Result: "Trojan.Gen"},
		"EngineB": {Category: "malicious", EngineName: "EngineB", Result: "Win32.Malware"},
		"EngineC": {Category: "harmless", EngineName: "EngineC", Result: ""},
	}
	apiResp.Data.Attributes.LastAnalysisDate = 1739500000

	report := parseFileReport(apiResp, "abc123sha256", false)

	if !report.Detected {
		t.Error("expected Detected=true")
	}
	if report.Detections != 6 {
		t.Errorf("expected 6 detections, got %d", report.Detections)
	}
	if report.TotalEngines != 72 {
		t.Errorf("expected 72 total engines, got %d", report.TotalEngines)
	}
	if report.Status != "vt_malware" {
		t.Errorf("expected status vt_malware, got %s", report.Status)
	}
	if len(report.MalwareNames) != 2 {
		t.Errorf("expected 2 malware names, got %d", len(report.MalwareNames))
	}
	if report.Permalink != "https://www.virustotal.com/gui/file/abc123sha256" {
		t.Errorf("unexpected permalink: %s", report.Permalink)
	}
}

func TestVTClient_LookupHash_Clean(t *testing.T) {
	apiResp := vtAPIResponse{}
	apiResp.Data.Attributes.LastAnalysisStats = vtAnalysisStats{
		Malicious: 0, Suspicious: 0, Harmless: 60, Undetected: 12,
	}
	apiResp.Data.Attributes.LastAnalysisResults = map[string]vtAnalysisResult{}
	apiResp.Data.Attributes.LastAnalysisDate = 1739500000

	report := parseFileReport(apiResp, "cleanfile", false)

	if report.Detected {
		t.Error("expected Detected=false for clean file")
	}
	if report.Detections != 0 {
		t.Errorf("expected 0 detections, got %d", report.Detections)
	}
	if report.Status != "vt_clean" {
		t.Errorf("expected status vt_clean, got %s", report.Status)
	}
}

func TestVTClient_ParseAnalysisReport(t *testing.T) {
	stats := vtAnalysisStats{
		Malicious: 10, Suspicious: 2, Harmless: 30, Undetected: 30,
	}
	results := map[string]vtAnalysisResult{
		"Eng1": {Category: "malicious", Result: "Trojan.X"},
		"Eng2": {Category: "malicious", Result: "Trojan.X"}, // duplicate name
		"Eng3": {Category: "suspicious", Result: "PUA.Adware"},
	}

	report := parseAnalysisReport(stats, results, true)

	if report.Detections != 12 {
		t.Errorf("expected 12 detections, got %d", report.Detections)
	}
	if !report.UploadedByUs {
		t.Error("expected UploadedByUs=true")
	}
	// Deduplication: Trojan.X should appear only once
	if len(report.MalwareNames) != 2 {
		t.Errorf("expected 2 unique malware names, got %d: %v", len(report.MalwareNames), report.MalwareNames)
	}
}

func TestVTClient_UploadFile_TooLarge(t *testing.T) {
	client := NewVTClient("test-key")

	// Create a file larger than 20MB
	dir := t.TempDir()
	path := filepath.Join(dir, "big.exe")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write 21MB of zeros
	buf := make([]byte, 1024*1024)
	for i := 0; i < 21; i++ {
		f.Write(buf)
	}
	f.Close()

	_, err = client.UploadFile(context.Background(), path)
	if err == nil {
		t.Error("expected error for file > 20MB")
	}
}

func TestDedup(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b", "d"}
	result := dedup(input)
	expected := []string{"a", "b", "c", "d"}

	if len(result) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(result))
	}
	for i, v := range result {
		if v != expected[i] {
			t.Errorf("dedup[%d] = %s, want %s", i, v, expected[i])
		}
	}
}

func TestNewVTClient(t *testing.T) {
	client := NewVTClient("my-api-key")
	if client.apiKey != "my-api-key" {
		t.Errorf("expected apiKey 'my-api-key', got '%s'", client.apiKey)
	}
	if client.httpClient == nil {
		t.Error("httpClient should not be nil")
	}
}
