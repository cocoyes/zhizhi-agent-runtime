package runtimeerr

import "fmt"

type Class string

const (
	ClassConfig     Class = "config"
	ClassModel      Class = "model"
	ClassPlan       Class = "plan"
	ClassCompile    Class = "compile"
	ClassCapability Class = "capability"
	ClassTool       Class = "tool"
	ClassTimeout    Class = "timeout"
	ClassCancelled  Class = "cancelled"
	ClassBudget     Class = "budget"
	ClassRuntime    Class = "runtime"
)

type Error struct {
	Code            string
	Class           Class
	Operation       string
	Message         string
	Retryable       bool
	AmbiguousEffect bool
	Cause           error
}

func (e *Error) Error() string {
	if e.Operation == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Operation, e.Message)
}
func (e *Error) Unwrap() error { return e.Cause }
func New(code string, class Class, operation, message string, cause error) *Error {
	return &Error{Code: code, Class: class, Operation: operation, Message: message, Cause: cause}
}
