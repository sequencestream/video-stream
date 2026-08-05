package visual

import (
	"encoding/json"
	"fmt"
	"strings"
)

// IdentityStack is the six-layer visual identity compiled into style_seed.
type IdentityStack struct {
	StyleRefURI     string   `json:"style_ref_uri"`
	Palette         []string `json:"palette"`
	LightingPreset  string   `json:"lighting_preset"`
	CompositionRule string   `json:"composition_rule"`
	BrandElements   []string `json:"brand_elements,omitempty"`
	SceneCards      []string `json:"scene_cards,omitempty"`
}

// StylePack is an L2 visual style pack (importable/exportable).
type StylePack struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	SchemaVersion int           `json:"schema_version"`
	Stack         IdentityStack `json:"stack"`
	StyleSeed     string        `json:"style_seed,omitempty"`
}

// Validate checks required fields.
func (p StylePack) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("style pack id must not be empty")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("style pack name must not be empty")
	}
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version is %d, want %d", p.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(p.Stack.StyleRefURI) == "" {
		return fmt.Errorf("style_ref_uri is required")
	}
	if len(p.Stack.Palette) < 2 {
		return fmt.Errorf("palette must have at least 2 colors")
	}
	for _, hex := range p.Stack.Palette {
		if !isHexColor(hex) {
			return fmt.Errorf("palette entry %q is not a valid HEX color", hex)
		}
	}
	if strings.TrimSpace(p.Stack.LightingPreset) == "" {
		return fmt.Errorf("lighting_preset is required")
	}
	if strings.TrimSpace(p.Stack.CompositionRule) == "" {
		return fmt.Errorf("composition_rule is required")
	}
	return nil
}

func isHexColor(s string) bool {
	s = strings.TrimPrefix(strings.ToLower(s), "#")
	return len(s) == 6 || len(s) == 3
}

// CompileStyleSeed builds the prompt injection string from the stack.
func CompileStyleSeed(stack IdentityStack) string {
	parts := []string{
		"style_ref:" + stack.StyleRefURI,
		"palette:" + strings.Join(stack.Palette, ","),
		"light:" + stack.LightingPreset,
		"composition:" + stack.CompositionRule,
	}
	if len(stack.BrandElements) > 0 {
		parts = append(parts, "brand:"+strings.Join(stack.BrandElements, "|"))
	}
	if len(stack.SceneCards) > 0 {
		parts = append(parts, "scenes:"+strings.Join(stack.SceneCards, "|"))
	}
	return strings.Join(parts, "; ")
}

// Seal fills derived fields.
func (p *StylePack) Seal() {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	p.StyleSeed = CompileStyleSeed(p.Stack)
}

// ExportJSON serialises the pack for import/export.
func (p StylePack) ExportJSON() ([]byte, error) {
	p.Seal()
	return json.MarshalIndent(p, "", "  ")
}

// ImportJSON parses an exported pack.
func ImportJSON(data []byte) (StylePack, error) {
	var p StylePack
	if err := json.Unmarshal(data, &p); err != nil {
		return StylePack{}, fmt.Errorf("parse style pack: %w", err)
	}
	p.Seal()
	if err := p.Validate(); err != nil {
		return StylePack{}, err
	}
	return p, nil
}

// StyleAnchorID returns the RenderProfile.style_anchor value for this pack.
func (p StylePack) StyleAnchorID() string {
	return "l2:" + p.ID
}
