package agent

import "testing"

func TestPolicyEngineEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		autonomy AutonomyLevel
		risk     RiskLevel
		want     PolicyDecisionType
	}{
		{"low autonomy requires approval for low risk", AutonomyLow, RiskLow, PolicyRequireApproval},
		{"medium autonomy allows low risk", AutonomyMedium, RiskLow, PolicyAllow},
		{"medium autonomy requires approval for medium risk", AutonomyMedium, RiskMedium, PolicyRequireApproval},
		{"high autonomy allows medium risk", AutonomyHigh, RiskMedium, PolicyAllow},
		{"high risk is denied", AutonomyHigh, RiskHigh, PolicyDeny},
	}

	engine := PolicyEngine{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.Evaluate(PolicyRequest{Autonomy: tt.autonomy, Risk: tt.risk})
			if got.Type != tt.want {
				t.Fatalf("decision=%s, want %s, reason=%s", got.Type, tt.want, got.Reason)
			}
		})
	}
}
