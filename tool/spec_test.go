package tool

import "testing"

func TestWriteSpecRequiresIdempotency(t *testing.T) {
	spec := Spec{ID: "send", Description: "send", SideEffect: SideEffectWriteNonIdempotent, Idempotency: IdempotencyUnknown}
	if err := spec.Validate(); err == nil {
		t.Fatal("expected unknown write idempotency to be rejected")
	}
}
func TestRiskConfirmation(t *testing.T) {
	spec := Spec{ID: "delete", Description: "delete", SideEffect: SideEffectDestructive, Idempotency: IdempotencyNonIdempotent, RiskLevel: RiskCritical, Confirmation: ConfirmationOnRisk}
	if !spec.RequiresConfirmation() {
		t.Fatal("destructive operation must require confirmation")
	}
}
