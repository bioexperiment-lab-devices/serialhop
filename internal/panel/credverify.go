package panel

import "context"

// CredVerifier abstracts the network probe so CredVerify can be
// unit-tested without going over the wire. The real implementation is
// the existing firstrun.go verifyCredentials path; bindings.go
// constructs the verifier that wraps it in a later task.
type CredVerifier interface {
	Verify(ctx context.Context, host, user, pass string) (CredsCheckKind, error)
}

// CredChange captures the inputs the save flow has at decision time:
// the on-disk-loaded user/pass (Old*), and the form's current values
// (New*). The save flow only triggers a network probe if either user
// or pass changed.
type CredChange struct {
	OldUser, OldPass          string
	NewHost, NewUser, NewPass string
}

// CredDecision is the panel-facing result returned to the SaveConfig
// binding. Outcome ∈ {"skipped", "ok", "unauthorized", "needs_confirm"}.
// Detail is populated for "needs_confirm" (the network error to
// surface to the operator).
type CredDecision struct {
	Outcome string
	Detail  string
}

// CredVerify decides what to do when SaveConfig sees an in-flight
// form. Pure: only the underlying Verify call hits the network.
type CredVerify struct {
	verifier CredVerifier
}

// NewCredVerifier constructs a CredVerify backed by the given verifier.
func NewCredVerifier(v CredVerifier) *CredVerify {
	return &CredVerify{verifier: v}
}

// Decide returns the action SaveConfig should take. When the operator
// hasn't touched user/pass, no network call is made (Outcome="skipped").
// Otherwise the verifier is consulted and its categorical result is
// translated into the panel's Outcome string.
func (c *CredVerify) Decide(ctx context.Context, ch CredChange) (CredDecision, error) {
	if ch.NewUser == ch.OldUser && ch.NewPass == ch.OldPass {
		return CredDecision{Outcome: "skipped"}, nil
	}
	kind, err := c.verifier.Verify(ctx, ch.NewHost, ch.NewUser, ch.NewPass)
	switch kind {
	case CredsOK:
		return CredDecision{Outcome: "ok"}, nil
	case CredsUnauthorized:
		return CredDecision{Outcome: "unauthorized"}, nil
	case CredsNeedsConfirm:
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		return CredDecision{Outcome: "needs_confirm", Detail: detail}, nil
	default:
		return CredDecision{Outcome: "unauthorized"}, nil
	}
}
