.PHONY: fmt vet test race leak build e2e-offline e2e-real example-smoke verify
fmt:
	go fmt ./...
vet:
	go vet ./...
test:
	go test ./...
race:
	go test -race ./...
leak:
	go test ./exec -run TestSchedulerRunsBatchConcurrently -count=1
build:
	go build ./...
e2e-offline:
	go test ./tests/e2e -run Offline -count=1
e2e-real:
	ZHIZHI_REAL_E2E=1 go test ./tests/e2e -run Real -count=1
example-smoke:
	go build ./examples/...
verify: fmt vet test e2e-offline example-smoke build race leak
