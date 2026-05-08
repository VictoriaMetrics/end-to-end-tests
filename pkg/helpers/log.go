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
func AddAllureLog(msg string) {
	ginkgo.AddReportEntry("LOG", ginkgo.ReportEntryVisibilityNever, ginkgo.Offset(1), msg)
}
