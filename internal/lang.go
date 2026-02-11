package internal

import (
	"sort"
	"strings"
)

// langNormalize maps ISO 639-2/B, 639-2/T, and 639-1 codes to ISO 639-1.
var langNormalize = map[string]string{
	"eng": "en", "en": "en",
	"spa": "es", "es": "es",
	"fre": "fr", "fra": "fr", "fr": "fr",
	"ger": "de", "deu": "de", "de": "de",
	"ita": "it", "it": "it",
	"por": "pt", "pt": "pt",
	"rus": "ru", "ru": "ru",
	"jpn": "ja", "ja": "ja",
	"kor": "ko", "ko": "ko",
	"chi": "zh", "zho": "zh", "zh": "zh",
	"hin": "hi", "hi": "hi",
	"ara": "ar", "ar": "ar",
	"dut": "nl", "nld": "nl", "nl": "nl",
	"pol": "pl", "pl": "pl",
	"tur": "tr", "tr": "tr",
	"swe": "sv", "sv": "sv",
	"nor": "no", "nob": "no", "nno": "no", "no": "no",
	"dan": "da", "da": "da",
	"fin": "fi", "fi": "fi",
	"cze": "cs", "ces": "cs", "cs": "cs",
	"hun": "hu", "hu": "hu",
	"rum": "ro", "ron": "ro", "ro": "ro",
	"gre": "el", "ell": "el", "el": "el",
	"tha": "th", "th": "th",
	"vie": "vi", "vi": "vi",
	"ind": "id", "id": "id",
	"heb": "he", "he": "he",
	"ukr": "uk", "uk": "uk",
	"cat": "ca", "ca": "ca",
	"bul": "bg", "bg": "bg",
	"hrv": "hr", "hr": "hr",
	"srp": "sr", "sr": "sr",
	"slv": "sl", "sl": "sl",
	"lit": "lt", "lt": "lt",
	"lav": "lv", "lv": "lv",
	"est": "et", "et": "et",
}

// NormalizeLang converts a language code to ISO 639-1.
// Returns the input lowercased if no mapping is found.
func NormalizeLang(raw string) string {
	if raw == "" {
		return "und"
	}
	lower := strings.ToLower(raw)
	if mapped, ok := langNormalize[lower]; ok {
		return mapped
	}
	return lower
}

// ComputeLanguages extracts unique ISO 639-1 language codes from audio tracks.
// It merges with any existing languages, replacing ambiguous tags like "multi"/"dual".
func ComputeLanguages(existing []string, audioTracks []AudioTrack) []string {
	detected := make(map[string]struct{})
	for _, t := range audioTracks {
		lang := t.Lang
		if lang != "" && lang != "und" && len(lang) <= 3 {
			detected[lang] = struct{}{}
		}
	}

	existingSet := make(map[string]struct{})
	for _, l := range existing {
		existingSet[l] = struct{}{}
	}

	// If existing was just "multi" or "dual", replace entirely with detected
	ambiguous := map[string]struct{}{"multi": {}, "dual": {}}
	allAmbiguous := true
	for l := range existingSet {
		if _, ok := ambiguous[l]; !ok {
			allAmbiguous = false
			break
		}
	}

	var merged map[string]struct{}
	if allAmbiguous && len(existingSet) > 0 {
		if len(detected) > 0 {
			merged = detected
		} else {
			merged = existingSet
		}
	} else {
		// Union of existing (minus ambiguous) and detected
		merged = make(map[string]struct{})
		for l := range existingSet {
			if _, ok := ambiguous[l]; !ok {
				merged[l] = struct{}{}
			}
		}
		for l := range detected {
			merged[l] = struct{}{}
		}
		if len(merged) == 0 {
			merged = existingSet
		}
	}

	result := make([]string, 0, len(merged))
	for l := range merged {
		result = append(result, l)
	}
	sort.Strings(result)
	return result
}
