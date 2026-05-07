/*
The following code was adapted from https://github.com/ramich2077/allure-ginkgo/
License: No explicit license found in original repository (All Rights Reserved).
*/

package allure

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

const logReportEntryName = "LOG"

// Log adds a log message that will appear in the allure report under the
// currently active step's logEntries.
func Log(message string) {
	entry := logEntry{
		Name:      "log",
		Message:   message,
		Timestamp: time.Now().UnixMilli(),
	}
	ginkgo.AddReportEntry(logReportEntryName, ginkgo.ReportEntryVisibilityNever, ginkgo.Offset(1), string(saveAsJSONLogEntry(&entry)))
}

func AddAttachment(name string, mimeType MimeType, content []byte) {
	a, _ := addAttachment(name, mimeType, content)
	// Here we are marshalling the attachment object itself to JSON, so it can be transferred between parallel processes
	ginkgo.AddReportEntry(attachmentReportEntryName, ginkgo.ReportEntryVisibilityNever, ginkgo.Offset(1), string(saveAsJSONAttachment(&a)))
}

func FromGinkgoReport(report types.Report) error {
	return newTestContainer().createFromReport(report).write()
}
