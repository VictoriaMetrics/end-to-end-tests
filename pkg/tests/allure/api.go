/*
The following code was adapted from https://github.com/ramich2077/allure-ginkgo/
License: No explicit license found in original repository (All Rights Reserved).
*/

package allure

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

func AddAttachment(name string, mimeType MimeType, content []byte) {
	a, _ := addAttachment(name, mimeType, content)
	// Here we are marshalling the attachment object itself to JSON, so it can be transferred between parallel processes
	ginkgo.AddReportEntry(attachmentReportEntryName, ginkgo.ReportEntryVisibilityNever, ginkgo.Offset(1), string(saveAsJSONAttachment(&a)))
}

func AddParameter(name string, value string) {
	p := parameter{Name: name, Value: value}
	ginkgo.AddReportEntry(parameterReportEntryName, ginkgo.ReportEntryVisibilityNever, ginkgo.Offset(1), string(saveAsJSONAttachment(&p)))
}

func FromGinkgoReport(report types.Report) error {
	return newTestContainer().createFromReport(report).write()
}
