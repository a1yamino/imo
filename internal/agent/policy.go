package agent

type PolicyEngine struct{}

func (PolicyEngine) Evaluate(req PolicyRequest) PolicyDecision {
	if req.Risk == RiskHigh {
		return PolicyDecision{Type: PolicyDeny, Reason: "high risk actions are denied in the MVP"}
	}

	switch req.Autonomy {
	case AutonomyLow:
		return PolicyDecision{Type: PolicyRequireApproval, Reason: "low autonomy requires approval for tool execution"}
	case AutonomyHigh:
		return PolicyDecision{Type: PolicyAllow, Reason: "high autonomy allows low and medium risk actions"}
	case AutonomyMedium, "":
		if req.Risk == RiskLow {
			return PolicyDecision{Type: PolicyAllow, Reason: "medium autonomy allows low risk actions"}
		}
		return PolicyDecision{Type: PolicyRequireApproval, Reason: "medium autonomy requires approval for medium risk actions"}
	default:
		return PolicyDecision{Type: PolicyRequireApproval, Reason: "unknown autonomy level requires approval"}
	}
}
