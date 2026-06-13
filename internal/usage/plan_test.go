package usage

import "testing"

func TestClassifyPlan(t *testing.T) {
	tests := []struct {
		in   string
		want Plan
	}{
		{"", PlanUnknown},
		{"  ", PlanUnknown},
		{"free", PlanFree},
		{"FREE", PlanFree},
		{"pro", PlanPro},
		{"Pro", PlanPro},
		{"max", PlanMax},
		{"max_5x", PlanMax5},
		{"max-5x", PlanMax5},
		{"max 5x", PlanMax5},
		{"max5x", PlanMax5},
		{"max_20x", PlanMax20},
		{"max-20x", PlanMax20},
		{"max20x", PlanMax20},
		{"team", PlanTeam},
		{"enterprise", PlanEnterprise},
		{"claude_max_20x", PlanMax20},
		{"claude_max_5", PlanMax5},
		{"some_max_thing", PlanMax},
		{"big_enterprise_seat", PlanEnterprise},
		{"team_plus", PlanTeam},
		{"pro_trial", PlanPro},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := ClassifyPlan(tc.in); got != tc.want {
				t.Errorf("ClassifyPlan(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClassifyPlanUnknownPreserved(t *testing.T) {
	got := ClassifyPlan("mystery tier")
	if got.Label() != "Mystery Tier" {
		t.Errorf("unknown plan label = %q, want %q", got.Label(), "Mystery Tier")
	}
}

func TestPlanLabel(t *testing.T) {
	tests := []struct {
		p    Plan
		want string
	}{
		{PlanUnknown, ""},
		{PlanFree, "Free"},
		{PlanPro, "Pro"},
		{PlanMax5, "Max 5x"},
		{PlanMax20, "Max 20x"},
		{PlanMax, "Max"},
		{PlanTeam, "Team"},
		{PlanEnterprise, "Enterprise"},
		{Plan("custom_label"), "Custom Label"},
	}
	for _, tc := range tests {
		t.Run(string(tc.p), func(t *testing.T) {
			if got := tc.p.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}
