// Package label injects mandatory implicit compliance identifiers after mux and
// verifies them by readback hash. There is no disable switch.
package label

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// ContentAttributeValue marks AI-generated synthetic media per platform policy.
	ContentAttributeValue = "AI_GENERATED"
	// ServiceProviderCode identifies this product in provider metadata.
	ServiceProviderCode = "sequencestream:video-stream"
)

var (
	// ErrReadbackMismatch means injected metadata could not be read back intact.
	ErrReadbackMismatch = errors.New("compliance label readback hash mismatch")
	// ErrBypassForbidden documents that label injection cannot be skipped.
	ErrBypassForbidden = errors.New("compliance label injection cannot be disabled or bypassed")
)

// Label is the three mandatory implicit identifier fields.
type Label struct {
	ContentAttribute    string `json:"content_attribute"`
	ServiceProviderCode string `json:"service_provider_code"`
	ContentID           string `json:"content_id"`
}

// Build derives label values for one rendered output.
func Build(projectID, runID string) Label {
	return Label{
		ContentAttribute:    ContentAttributeValue,
		ServiceProviderCode: ServiceProviderCode,
		ContentID:           fmt.Sprintf("%s:%s", projectID, runID),
	}
}

// Hash returns a stable digest of the canonical label JSON.
func Hash(l Label) string {
	b, _ := json.Marshal(l)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
