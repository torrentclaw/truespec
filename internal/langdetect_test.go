package internal

import (
	"testing"
)

func TestShouldDetectLanguage_SingleUnd(t *testing.T) {
	result := &ScanResult{
		Status: "success",
		Audio: []AudioTrack{
			{Lang: "und", Codec: "aac", Channels: 2},
		},
	}

	if !ShouldDetectLanguage(result) {
		t.Error("should detect language for single 'und' track")
	}
}

func TestShouldDetectLanguage_SingleEmpty(t *testing.T) {
	result := &ScanResult{
		Status: "success",
		Audio: []AudioTrack{
			{Lang: "", Codec: "aac", Channels: 2},
		},
	}

	if !ShouldDetectLanguage(result) {
		t.Error("should detect language for single empty lang track")
	}
}

func TestShouldDetectLanguage_MultipleUnd(t *testing.T) {
	result := &ScanResult{
		Status: "success",
		Audio: []AudioTrack{
			{Lang: "und", Codec: "aac", Channels: 2},
			{Lang: "und", Codec: "ac3", Channels: 6},
		},
	}

	if !ShouldDetectLanguage(result) {
		t.Error("should detect language when ALL tracks are und")
	}
}

func TestShouldDetectLanguage_MixedKnownAndUnd(t *testing.T) {
	result := &ScanResult{
		Status: "success",
		Audio: []AudioTrack{
			{Lang: "en", Codec: "aac", Channels: 2},
			{Lang: "und", Codec: "ac3", Channels: 6},
		},
	}

	if ShouldDetectLanguage(result) {
		t.Error("should NOT detect when at least one track has known language")
	}
}

func TestUndefinedTrackIndices(t *testing.T) {
	audio := []AudioTrack{
		{Lang: "und", Codec: "aac", Channels: 2},
		{Lang: "en", Codec: "ac3", Channels: 6},
		{Lang: "", Codec: "aac", Channels: 2},
	}

	indices := undefinedTrackIndices(audio)
	if len(indices) != 2 {
		t.Errorf("expected 2 undefined indices, got %d", len(indices))
	}
	if indices[0] != 0 || indices[1] != 2 {
		t.Errorf("expected indices [0, 2], got %v", indices)
	}
}

func TestUndefinedTrackIndices_AllUnd(t *testing.T) {
	audio := []AudioTrack{
		{Lang: "und", Codec: "aac", Channels: 2},
		{Lang: "unknown", Codec: "ac3", Channels: 6},
		{Lang: "", Codec: "eac3", Channels: 8},
	}

	indices := undefinedTrackIndices(audio)
	if len(indices) != 3 {
		t.Errorf("expected 3 undefined indices, got %d", len(indices))
	}
}

func TestEffectiveMaxTracks_Default(t *testing.T) {
	cfg := LangDetectConfig{}
	if got := effectiveMaxTracks(cfg); got != DefaultWhisperMaxTracks {
		t.Errorf("expected default %d, got %d", DefaultWhisperMaxTracks, got)
	}
}

func TestEffectiveMaxTracks_Custom(t *testing.T) {
	cfg := LangDetectConfig{MaxTracks: 5}
	if got := effectiveMaxTracks(cfg); got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
}

func TestEffectiveMaxTracks_Zero(t *testing.T) {
	cfg := LangDetectConfig{MaxTracks: 0}
	if got := effectiveMaxTracks(cfg); got != DefaultWhisperMaxTracks {
		t.Errorf("expected default %d for zero value, got %d", DefaultWhisperMaxTracks, got)
	}
}

func TestShouldDetectLanguage_KnownLang(t *testing.T) {
	result := &ScanResult{
		Status: "success",
		Audio: []AudioTrack{
			{Lang: "en", Codec: "aac", Channels: 2},
		},
	}

	if ShouldDetectLanguage(result) {
		t.Error("should NOT detect language when lang is known")
	}
}

func TestShouldDetectLanguage_FailedScan(t *testing.T) {
	result := &ScanResult{
		Status: "stall_metadata",
		Audio: []AudioTrack{
			{Lang: "und", Codec: "aac", Channels: 2},
		},
	}

	if ShouldDetectLanguage(result) {
		t.Error("should NOT detect language for failed scans")
	}
}

func TestShouldDetectLanguage_NilResult(t *testing.T) {
	if ShouldDetectLanguage(nil) {
		t.Error("should NOT detect language for nil result")
	}
}

func TestShouldDetectLanguage_NoAudio(t *testing.T) {
	result := &ScanResult{
		Status: "success",
		Audio:  []AudioTrack{},
	}

	if ShouldDetectLanguage(result) {
		t.Error("should NOT detect language when no audio tracks")
	}
}

func TestConfidenceRegex(t *testing.T) {
	tests := []struct {
		input    string
		lang     string
		conf     float64
		hasMatch bool
	}{
		{
			"auto-detected language: en (p = 0.409680)",
			"en", 0.409680, true,
		},
		{
			"auto-detected language: es (p = 0.923456)",
			"es", 0.923456, true,
		},
		{
			"some other output",
			"", 0, false,
		},
	}

	for _, tt := range tests {
		matches := confidenceRe.FindStringSubmatch(tt.input)
		if tt.hasMatch {
			if len(matches) != 3 {
				t.Errorf("expected match for %q, got none", tt.input)
				continue
			}
			if matches[1] != tt.lang {
				t.Errorf("expected lang %s, got %s", tt.lang, matches[1])
			}
		} else {
			if len(matches) > 0 {
				t.Errorf("expected no match for %q, got %v", tt.input, matches)
			}
		}
	}
}

func TestResolveLangDetect_NoWhisper(t *testing.T) {
	// With no whisper installed in test env, should return Enabled=false
	cfg := ResolveLangDetect()
	// We can't guarantee whisper IS or ISN'T installed, but if it is, Enabled should be true
	// This test mainly verifies it doesn't panic
	_ = cfg
}
