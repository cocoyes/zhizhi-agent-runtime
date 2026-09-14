package speech

import (
	"context"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

func TestRegistryToolSetRejectsConfirmationByDefault(t *testing.T) {
	type input struct {
		Value string `json:"value"`
	}
	value := tool.Func("mail.send", "send mail", func(context.Context, input) (string, error) { return "sent", nil },
		tool.WithSideEffect(tool.SideEffectWriteNonIdempotent),
		tool.WithIdempotency(tool.IdempotencyNonIdempotent),
		tool.WithConfirmation(tool.ConfirmationAlways),
	)
	set, err := RegistryToolSet(tool.NewRegistry(value), nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Definitions[0].Name != "mail_send" {
		t.Fatalf("wire name = %q", set.Definitions[0].Name)
	}
	if _, err := set.Executor.Execute(context.Background(), FunctionCall{Name: "mail_send", Arguments: `{"value":"x"}`}); err == nil {
		t.Fatal("expected authorization error")
	}
}
