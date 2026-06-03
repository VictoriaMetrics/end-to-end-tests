package allure

import "testing"

func TestCommandLineFromArgsQuotesArguments(t *testing.T) {
	got := commandLineFromArgs([]string{"go", "test", "./tests/load test", "-run", "Test'Name"})
	want := "go test './tests/load test' -run 'Test'\\''Name'"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestResultAddsReportParameters(t *testing.T) {
	r := newResult()
	r.addReportParameters("abc123", "go test ./...")

	if len(r.Parameters) != 2 {
		t.Fatalf("expected 2 parameters, got %d", len(r.Parameters))
	}
	if r.Parameters[0].Name != "git_hash" || r.Parameters[0].Value != "abc123" {
		t.Fatalf("unexpected git hash parameter: %#v", r.Parameters[0])
	}
	if r.Parameters[1].Name != "command_line" || r.Parameters[1].Value != "go test ./..." {
		t.Fatalf("unexpected command line parameter: %#v", r.Parameters[1])
	}
}
