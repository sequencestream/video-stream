package compliance

import (
	"fmt"
	"strings"
)

// CheckNonTemplateGate requires at least one non-template element.
func CheckNonTemplateGate(elements []NonTemplateElement, scriptText string) GateResult {
	const gate = "non_template_element"
	if len(elements) == 0 {
		return GateResult{
			Gate: gate, Passed: false,
			Reason: "no non-template element declared",
			Advice: "add at least one user quote, first-hand data point, or exclusive source reference",
		}
	}
	lower := strings.ToLower(scriptText)
	for _, e := range elements {
		if !e.Kind.valid() {
			return GateResult{
				Gate: gate, Passed: false,
				Reason: fmt.Sprintf("unknown non-template kind %q", e.Kind),
				Advice: "use user_quote, first_hand_data, or exclusive_source",
			}
		}
		if strings.TrimSpace(e.Content) == "" {
			return GateResult{
				Gate: gate, Passed: false,
				Reason: "non-template element has empty content",
				Advice: "provide the actual quote, data, or source identifier",
			}
		}
		// Content must appear in script (or evidence field) — prevents checkbox fraud.
		needle := strings.ToLower(e.Content)
		inScript := strings.Contains(lower, needle)
		inEvidence := e.Evidence != "" && strings.Contains(strings.ToLower(scriptText+" "+e.Evidence), needle)
		if !inScript && !inEvidence {
			return GateResult{
				Gate: gate, Passed: false,
				Reason: fmt.Sprintf("%s element not found in script", e.Kind),
				Advice: " weave the declared element into the script text before rendering",
			}
		}
	}
	return GateResult{Gate: gate, Passed: true, Metric: fmt.Sprintf("elements=%d", len(elements))}
}

func (k NonTemplateKind) valid() bool {
	switch k {
	case KindUserQuote, KindFirstHandData, KindExclusiveSource:
		return true
	default:
		return false
	}
}
