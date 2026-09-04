package zhizhi_test

import (
	"errors"
	"testing"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
)

func TestRuntimeErrorRemainsAvailableFromPublicFacade(t *testing.T) {
	err := error(&zhizhi.RuntimeError{Code: "RUNTIME_ERROR", Class: zhizhi.ErrorClassRuntime, Message: "failed"})
	var structured *zhizhi.RuntimeError
	if !errors.As(err, &structured) {
		t.Fatalf("expected public structured error, got %T", err)
	}
	if structured.Class != zhizhi.ErrorClassRuntime {
		t.Fatalf("unexpected error class: %q", structured.Class)
	}
}
