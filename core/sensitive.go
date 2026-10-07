package main

import "strings"

// Sensitive keyword detection for clipboard capture.
//
// Rule (IPC.md §捕获规则-4): when settings.skip_sensitive != "off", any text
// containing one of these keywords (case-insensitive) triggers the
// ask/auto flow. Images are never OCR'd; they are only skipped when
// skip_sensitive == "auto".
var sensitiveKeywords = []string{
	"密码", "口令", "验证码", "支付", "银行", "信用卡",
	"token", "passwd", "pwd",
}

// IsSensitive reports whether text contains any sensitive keyword.
// Matching is case-insensitive for ASCII keywords.
func IsSensitive(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range sensitiveKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// SensitivePreview returns a short redacted preview for the prompt UI.
func SensitivePreview(text string) string {
	const maxRunes = 60
	r := []rune(text)
	if len(r) > maxRunes {
		r = r[:maxRunes]
	}
	return string(r)
}
