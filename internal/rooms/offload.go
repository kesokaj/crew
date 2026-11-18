package rooms

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// OffloadThresholdRunes is the rune count above which agent prompt deliveries receive a
	// compact preview + SHA-256 pointer instead of full prose inline.
	OffloadThresholdRunes = 4000
	// OffloadPreviewBytes caps inline UTF-8 preview bytes in agent deliveries when offloaded.
	OffloadPreviewBytes = 768
)

var reservedMetadataKeys = []string{
	"fenced",
	"verified",
	"provenance",
	"sender_role",
	"sender_handle",
	"hop_count",
	"body_sha256",
	"body_runes",
	"offloaded",
	"delivery_state",
	"task_id",
	"spawned_session",
}

// StripReservedMetadata drops internal Crew metadata keys from caller-supplied message metadata so
// senders cannot spoof provenance, delivery, or verification fields on ingress.
func StripReservedMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		low := strings.ToLower(strings.TrimSpace(k))
		if low == "" || strings.HasPrefix(low, "crew_") || strings.HasPrefix(low, "_crew_") || slices.Contains(reservedMetadataKeys, low) {
			continue
		}
		out[k] = v
	}
	return out
}

// ComputeBodyDigest returns the lowercase SHA-256 hex digest and rune length of cleanBody.
func ComputeBodyDigest(body string) (shaHex string, runes int) {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:]), utf8.RuneCountInString(body)
}

// FormatAgentDeliveryBody returns body verbatim when <= OffloadThresholdRunes, or a UTF-8 safe
// <= 768-byte preview plus SHA-256 and read_chat fetch hint when oversized.
func FormatAgentDeliveryBody(msgID int64, body, shaHex string) (text string, offloaded bool) {
	runes := utf8.RuneCountInString(body)
	if runes <= OffloadThresholdRunes {
		return body, false
	}
	if shaHex == "" {
		shaHex, _ = ComputeBodyDigest(body)
	}
	preview := utf8Prefix(body, OffloadPreviewBytes)
	if nl := strings.LastIndexByte(preview, '\n'); nl >= OffloadPreviewBytes/2 {
		preview = preview[:nl]
	}
	preview = strings.TrimRight(preview, " \t\n")
	return fmt.Sprintf("%s\n\n[offloaded: %d runes, sha256=%s; fetch full message with read_chat(messageId=%d)]",
		preview, runes, shaHex, msgID), true
}

func utf8Prefix(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
