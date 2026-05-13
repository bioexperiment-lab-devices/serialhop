package panel

import (
	"context"
	"errors"
	"testing"
)

type fakeVerifier struct {
	calls   int
	outcome CredsCheckKind
	err     error
}

func (f *fakeVerifier) Verify(_ context.Context, _, _, _ string) (CredsCheckKind, error) {
	f.calls++
	return f.outcome, f.err
}

func TestCredVerify_UnchangedCredentialsSkipNetwork(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsOK}
	cv := NewCredVerifier(fv)
	res, err := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "pw",
		NewHost: "h", NewUser: "alice", NewPass: "pw",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Outcome != "skipped" {
		t.Errorf("Outcome: got %q, want skipped", res.Outcome)
	}
	if fv.calls != 0 {
		t.Errorf("Verify called %d times — expected 0 (no change)", fv.calls)
	}
}

func TestCredVerify_PasswordChangedTriggersVerify_OK(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsOK}
	cv := NewCredVerifier(fv)
	res, _ := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "old",
		NewHost: "h", NewUser: "alice", NewPass: "new",
	})
	if res.Outcome != "ok" {
		t.Errorf("Outcome: got %q, want ok", res.Outcome)
	}
	if fv.calls != 1 {
		t.Errorf("Verify calls: got %d, want 1", fv.calls)
	}
}

func TestCredVerify_UserChangedAndUnauthorized(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsUnauthorized}
	cv := NewCredVerifier(fv)
	res, _ := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "pw",
		NewHost: "h", NewUser: "bob", NewPass: "pw",
	})
	if res.Outcome != "unauthorized" {
		t.Errorf("Outcome: got %q, want unauthorized", res.Outcome)
	}
}

func TestCredVerify_NetworkErrorSurfacesNeedsConfirm(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsNeedsConfirm, err: errors.New("dial tcp: connection refused")}
	cv := NewCredVerifier(fv)
	res, _ := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "pw",
		NewHost: "h", NewUser: "alice", NewPass: "pw2",
	})
	if res.Outcome != "needs_confirm" {
		t.Errorf("Outcome: got %q, want needs_confirm", res.Outcome)
	}
	if res.Detail == "" {
		t.Errorf("Detail empty — operator needs network-error text")
	}
}
