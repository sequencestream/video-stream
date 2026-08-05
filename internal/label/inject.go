package label

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Injector writes implicit identifiers onto a muxed output and verifies readback.
type Injector interface {
	Inject(path string, l Label) error
	Readback(path string) (Label, error)
}

// SidecarInjector stores labels in a sidecar JSON file keyed to the output path.
// Production may swap this for FFmpeg metadata injection; readback semantics stay the same.
type SidecarInjector struct{}

func sidecarPath(outputPath string) string {
	return outputPath + ".vslabel.json"
}

// Inject writes the label sidecar including its expected hash.
func (SidecarInjector) Inject(outputPath string, l Label) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	payload := struct {
		Label Label  `json:"label"`
		Hash  string `json:"hash"`
	}{Label: l, Hash: Hash(l)}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(sidecarPath(outputPath), b, 0o644)
}

// Readback loads and verifies the label from the sidecar.
func (s SidecarInjector) Readback(outputPath string) (Label, error) {
	b, err := os.ReadFile(sidecarPath(outputPath))
	if err != nil {
		return Label{}, err
	}
	var payload struct {
		Label Label  `json:"label"`
		Hash  string `json:"hash"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return Label{}, err
	}
	if payload.Hash != Hash(payload.Label) {
		return Label{}, ErrReadbackMismatch
	}
	return payload.Label, nil
}

// InjectAndVerify injects then readbacks; mismatch rejects the output.
func InjectAndVerify(inj Injector, outputPath string, want Label) error {
	if inj == nil {
		return ErrBypassForbidden
	}
	if err := inj.Inject(outputPath, want); err != nil {
		return fmt.Errorf("inject label: %w", err)
	}
	got, err := inj.Readback(outputPath)
	if err != nil {
		return err
	}
	if got != want {
		return ErrReadbackMismatch
	}
	if Hash(got) != Hash(want) {
		return ErrReadbackMismatch
	}
	return nil
}

// TamperSidecar corrupts the stored hash for tests simulating metadata loss.
func TamperSidecar(outputPath string) error {
	path := sidecarPath(outputPath)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		return err
	}
	payload["hash"] = "deadbeef"
	out, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// VerifyReadback checks an existing output without injecting.
func VerifyReadback(inj Injector, outputPath string, want Label) error {
	got, err := inj.Readback(outputPath)
	if err != nil {
		return err
	}
	if got != want || Hash(got) != Hash(want) {
		return ErrReadbackMismatch
	}
	return nil
}

// IsReadbackMismatch reports label verification failures.
func IsReadbackMismatch(err error) bool {
	return errors.Is(err, ErrReadbackMismatch)
}
