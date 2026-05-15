package helpers

import (
	"fmt"
	"github.com/onsi/ginkgo/v2"
)

// Logf formats a message, prints it to stdout, and adds it to the current Allure step.
func Logf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(msg)
	AddAllureLog(msg)
}

// AddAllureLog adds a message to the current Allure step without printing to stdout.
// No-op when called outside a running Ginkgo spec (e.g. from plain go test).
func AddAllureLog(msg string) {
	defer func() { recover() }() //nolint:errcheck
	if ginkgo.CurrentSpecReport().LeafNodeType == 0 {
		return
	}
	ginkgo.AddReportEntry("LOG", ginkgo.ReportEntryVisibilityNever, ginkgo.Offset(1), msg)
}
