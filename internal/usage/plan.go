package usage

import "strings"

// Plan is a best-effort classification of the account's subscription tier,
// derived from the raw subscriptionType string carried by the OAuth
// credentials. The set of strings the credential store uses is not part of any
// public contract, so unknown values are preserved verbatim via [Plan.Label].
type Plan string

const (
	// PlanUnknown means the subscription type was empty or unrecognized.
	PlanUnknown Plan = ""
	// PlanFree is the free tier.
	PlanFree Plan = "free"
	// PlanPro is the Pro subscription.
	PlanPro Plan = "pro"
	// PlanMax5 is the Max 5x subscription.
	PlanMax5 Plan = "max_5x"
	// PlanMax20 is the Max 20x subscription.
	PlanMax20 Plan = "max_20x"
	// PlanMax is a Max subscription whose multiplier could not be determined.
	PlanMax Plan = "max"
	// PlanTeam is a Team seat.
	PlanTeam Plan = "team"
	// PlanEnterprise is an Enterprise seat.
	PlanEnterprise Plan = "enterprise"
)

// raw retains the original subscriptionType string for plans that did not map
// to a known constant, so [Plan.Label] can still render something useful.
//
// ClassifyPlan stores the original string inside the returned Plan value when
// it does not match a known tier; this keeps Plan a plain string type while
// preserving the source text for display.

// ClassifyPlan maps a raw subscriptionType string to a [Plan]. Matching is
// case-insensitive and tolerant of common separators ("max-5x", "max_5x",
// "max 5x"). Unrecognized non-empty values are returned as-is so the original
// text can still be shown to the user.
func ClassifyPlan(subscriptionType string) Plan {
	s := strings.ToLower(strings.TrimSpace(subscriptionType))
	if s == "" {
		return PlanUnknown
	}
	norm := strings.NewReplacer("-", "_", " ", "_").Replace(s)
	switch norm {
	case "free":
		return PlanFree
	case "pro":
		return PlanPro
	case "max_5x", "max5x", "max_5":
		return PlanMax5
	case "max_20x", "max20x", "max_20":
		return PlanMax20
	case "max":
		return PlanMax
	case "team":
		return PlanTeam
	case "enterprise":
		return PlanEnterprise
	}
	// Heuristics for composite strings such as "claude_max_20x".
	switch {
	case strings.Contains(norm, "max_20") || strings.Contains(norm, "max20"):
		return PlanMax20
	case strings.Contains(norm, "max_5") || strings.Contains(norm, "max5"):
		return PlanMax5
	case strings.Contains(norm, "max"):
		return PlanMax
	case strings.Contains(norm, "enterprise"):
		return PlanEnterprise
	case strings.Contains(norm, "team"):
		return PlanTeam
	case strings.Contains(norm, "pro"):
		return PlanPro
	}
	// Unknown: preserve the original (trimmed) text.
	return Plan(strings.TrimSpace(subscriptionType))
}

// Label returns a human-readable name for the plan suitable for display.
func (p Plan) Label() string {
	switch p {
	case PlanUnknown:
		return ""
	case PlanFree:
		return "Free"
	case PlanPro:
		return "Pro"
	case PlanMax5:
		return "Max 5x"
	case PlanMax20:
		return "Max 20x"
	case PlanMax:
		return "Max"
	case PlanTeam:
		return "Team"
	case PlanEnterprise:
		return "Enterprise"
	default:
		// Unknown plan: title-case the preserved raw string.
		return titleWords(string(p))
	}
}

// titleWords upper-cases the first letter of each whitespace- or
// underscore-separated word.
func titleWords(s string) string {
	// strings.FieldsFunc never yields empty fields, so every f has at least one
	// rune and f[:1] is always safe.
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-'
	})
	for i, f := range fields {
		fields[i] = strings.ToUpper(f[:1]) + f[1:]
	}
	return strings.Join(fields, " ")
}
