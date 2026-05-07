/*
The following code was adapted from https://github.com/ramich2077/allure-ginkgo/
License: No explicit license found in original repository (All Rights Reserved).
*/

package allure

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

const descriptionReportEntryName = "DESCRIPTION"

// result is the top level report object for a test.
type result struct {
	UUID          string         `json:"uuid,omitempty"`
	TestCaseID    string         `json:"testCaseId,omitempty"`
	HistoryID     string         `json:"historyId,omitempty"`
	Name          string         `json:"name,omitempty"`
	Description   string         `json:"description,omitempty"`
	Status        string         `json:"status,omitempty"`
	StatusDetails *statusDetails `json:"statusDetails,omitempty"`
	Stage         string         `json:"stage,omitempty"`
	Steps         []stepObject   `json:"steps,omitempty"`
	Attachments   []attachment   `json:"attachments,omitempty"`
	Start         int64          `json:"start,omitempty"`
	Stop          int64          `json:"stop,omitempty"`
	Children      []string       `json:"children,omitempty"`
	FullName      string         `json:"fullName,omitempty"`
	Labels        []label        `json:"labels,omitempty"`
	Suite         string         `json:"-"`
	ParentSuite   string         `json:"-"`
}

func (r *result) addSuite(suite string) {
	r.Suite = suite
	r.addLabel(labelSuite, suite)
}

func (r *result) addParentSuite(parentSuite string) *result {
	r.ParentSuite = parentSuite
	r.addLabel(labelParentSuite, parentSuite)

	return r
}

func (r *result) addAttachment(attachment *attachment) *result {
	if attachment == nil {
		panic(fmt.Errorf("nil attachment pointer"))
	}

	r.Attachments = append(r.Attachments, *attachment)

	return r
}

func (r *result) addFullName(fullName string) {
	r.FullName = fullName
}

func (r *result) addLabel(name string, value string) {
	r.Labels = append(r.Labels, label{
		Name:  name,
		Value: value,
	})
}

func (r *result) setStatusDetails(details statusDetails) *result {
	r.StatusDetails = &details

	return r
}

func (r *result) createFromSpecReport(specReport ginkgo.SpecReport) *result {
	r.Start = getTimestampMsFromTime(specReport.StartTime)
	r.Stop = getTimestampMsFromTime(specReport.EndTime)

	if r.Stop < r.Start { // Workaround for incorrect skipped tests execution time
		r.Stop = r.Start
	}

	r.Name = specReport.LeafNodeText
	r.Description = buildDescription(specReport)

	r.setDefaultLabels(specReport)

	if len(specReport.ContainerHierarchyTexts) > 0 {
		r.addSuite(specReport.ContainerHierarchyTexts[len(specReport.ContainerHierarchyTexts)-1])
	} else {
		r.addSuite(r.Name)
	}

	currentHash := uuid.NewSHA1(
		uuid.Nil, []byte(strings.Join([]string{r.Name, r.Suite, r.ParentSuite}, ""))).String()
	r.TestCaseID = currentHash
	r.HistoryID = currentHash

	r.Stage = "finished"
	r.Status = getTestStatus(specReport)

	var failureOrder int
	if r.Status == failed || r.Status == broken {
		message := specReport.Failure.Message
		if message == "" {
			message = specReport.Failure.ForwardedPanic
		}
		details := statusDetails{
			Message: extractErrorMessage(message),
			Trace:   specReport.Failure.Location.FullStackTrace,
		}
		r.setStatusDetails(details)
		failureOrder = specReport.Failure.TimelineLocation.Order
	}

	attachmentEntries := filterForAttachments(specReport.ReportEntries)
	logEntries := filterForLogs(specReport.ReportEntries)
	var toSkip map[int]struct{}
	r.Steps, toSkip = createSteps(specReport.SpecEvents, attachmentEntries, logEntries, failureOrder)

	for i, entry := range attachmentEntries {
		if _, ok := toSkip[i]; !ok {

			var att attachment
			err := json.Unmarshal([]byte(entry.Value.GetRawValue().(string)), &att)

			if err != nil {
				panic(fmt.Errorf("error processing attachment for entry %s on line %d", entry.Location.FileName, entry.Location.LineNumber))
			} else if reflect.DeepEqual(att, attachment{}) {
				panic(fmt.Errorf("nil pointer attachment for entry %s on line %d", entry.Location.FileName, entry.Location.LineNumber))
			}

			r.addAttachment(&att)
		}
	}

	return r
}

// extractErrorMessage extracts the human-readable error from testify's formatted
// failure message. Testify formats failures as:
//
//	\n\tError Trace:\t<file>:<line>\n\tError:\t<message>\n\tTest:\t...
//
// The Allure UI shows the first non-empty line as the error title, which would
// otherwise render as a file path. We extract everything from "Error:" up to
// (but not including) the "Test:" line, preserving multi-line messages like
// "Not equal:\n\texpected: ...\n\tactual: ...".
func extractErrorMessage(msg string) string {
	lines := strings.Split(msg, "\n")
	collecting := false
	var parts []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !collecting {
			if after, ok := strings.CutPrefix(trimmed, "Error:"); ok {
				collecting = true
				if clean := strings.TrimSpace(after); clean != "" {
					parts = append(parts, clean)
				}
			}
		} else {
			if strings.HasPrefix(trimmed, "Test:") {
				break
			}
			parts = append(parts, trimmed)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return strings.TrimSpace(msg)
}

func createSteps(events types.SpecEvents, entries types.ReportEntries, logs types.ReportEntries, failureOrder int) (steps []stepObject, indicesToSkip map[int]struct{}) {
	currentEndIndex := -1
	indicesToSkip = make(map[int]struct{})
	steps = []stepObject{}

	for startEventIndex, startEvent := range events {
		if currentEndIndex >= startEventIndex {
			// Skipping all nested steps from previous iterations
			continue
		}

		if startEvent.SpecEventType == types.SpecEventByStart {
			step := newStep()
			step.addName(startEvent.Message)
			step.Status = passed
			step.Stage = "finished"
			endEvent, endIndex := findByEventEnd(events, startEvent)

			if endEvent != nil {
				step.Start = getTimestampMsFromTime(startEvent.TimelineLocation.Time)
				step.Stop = getTimestampMsFromTime(endEvent.TimelineLocation.Time)

				if failureOrder > 0 &&
					failureOrder > startEvent.TimelineLocation.Order &&
					failureOrder <= endEvent.TimelineLocation.Order {
					step.Status = failed
				}

				childrenSteps, toSkip := createSteps(events[startEventIndex+1:endIndex], entries, logs, failureOrder)

				step.ChildrenSteps = childrenSteps

				for i, entry := range entries {
					if _, ok := toSkip[i]; !ok {
						if entry.TimelineLocation.Order > startEvent.TimelineLocation.Order &&
							entry.TimelineLocation.Order < endEvent.TimelineLocation.Order {
							var att attachment
							err := json.Unmarshal([]byte(entry.Value.GetRawValue().(string)), &att)
							if err != nil {
								panic(fmt.Errorf("error processing attachment for entry %s on line %d", entry.Location.FileName, entry.Location.LineNumber))
							} else if reflect.DeepEqual(att, attachment{}) {
								panic(fmt.Errorf("nil pointer attachment for entry %s on line %d", entry.Location.FileName, entry.Location.LineNumber))
							}
							step.addAttachment(&att)

							toSkip[i] = struct{}{}
						}
					}
				}

				for _, logRE := range logs {
					if logRE.TimelineLocation.Order > startEvent.TimelineLocation.Order &&
						logRE.TimelineLocation.Order < endEvent.TimelineLocation.Order {
						var le logEntry
						if err := json.Unmarshal([]byte(logRE.Value.GetRawValue().(string)), &le); err != nil {
							panic(fmt.Errorf("error processing log entry %s on line %d", logRE.Location.FileName, logRE.Location.LineNumber))
						}
						step.addLogEntry(le)
					}
				}

				for k, v := range toSkip {
					indicesToSkip[k] = v
				}
				currentEndIndex = endIndex
			}

			steps = append(steps, *step)
		}
	}
	return steps, indicesToSkip
}

func findByEventEnd(events types.SpecEvents, startEvent types.SpecEvent) (event *types.SpecEvent, index int) {
	for i, e := range events {
		if e.SpecEventType == types.SpecEventByEnd &&
			startEvent.CodeLocation.LineNumber == e.CodeLocation.LineNumber &&
			startEvent.TimelineLocation.Order < e.TimelineLocation.Order {
			return &e, i
		}
	}

	return nil, -1
}

func filterForAttachments(entries types.ReportEntries) types.ReportEntries {
	var res types.ReportEntries
	for _, entry := range entries {
		if entry.Name == attachmentReportEntryName {
			res = append(res, entry)
		}
	}

	return res
}

func filterForLogs(entries types.ReportEntries) types.ReportEntries {
	var res types.ReportEntries
	for _, entry := range entries {
		if entry.Name == logReportEntryName {
			res = append(res, entry)
		}
	}

	return res
}

func buildDescription(specReport ginkgo.SpecReport) string {
	containerDescs := make([]string, 0)
	if len(specReport.ContainerHierarchyTexts) > 1 {
		// every container text excluding the top-level suite desc
		containerDescs = append(containerDescs, specReport.ContainerHierarchyTexts[1:]...)
	}

	var nodeDesc string
	for _, entry := range specReport.ReportEntries {
		if entry.Name == descriptionReportEntryName {
			nodeDesc = entry.Value.GetRawValue().(string)
		}
	}

	return strings.Join(append(containerDescs, nodeDesc), "\n")
}

func (r *result) setDefaultLabels(report ginkgo.SpecReport) *result {
	wsd := os.Getenv(wsPathEnvKey)

	programCounters := make([]uintptr, 10)
	callersCount := runtime.Callers(0, programCounters)
	var testFile string
	for i := 0; i < callersCount; i++ {
		_, testFile, _, _ = runtime.Caller(i)
		if strings.Contains(testFile, "_test.go") {
			break
		}
	}
	testPackage := strings.TrimSuffix(strings.ReplaceAll(strings.TrimPrefix(testFile, wsd+"/"), "/", "."), ".go")

	if report.IsSerial {
		r.addLabel("thread", "0")
	} else {
		r.addLabel("thread", strconv.Itoa(report.ParallelProcess))
	}

	r.addLabel("package", testPackage)
	r.addLabel("testClass", testPackage)
	r.addLabel("testMethod", report.LeafNodeText)
	if len(wsd) == 0 {
		r.addFullName(fmt.Sprintf("%s:%s", report.FileName(), report.LeafNodeText))
	} else {
		r.addFullName(fmt.Sprintf("%s:%s", strings.TrimPrefix(report.FileName(), wsd+"/"), report.LeafNodeText))
	}
	if hostname, err := os.Hostname(); err == nil {
		r.addLabel("host", hostname)
	}

	r.addLabel("language", "golang")

	return r
}

func (r *result) write() {
	content, err := json.Marshal(r)
	if err != nil {
		panic(fmt.Errorf("failed to marshall result into MimeTypeJSON: %w", err))
	}

	err = writeFile(fmt.Sprintf("%s-result.json", r.TestCaseID), content)
	if err != nil {
		panic(fmt.Errorf("failed to write content of result to json file: %w", err))
	}
}

func newResult() *result {
	return &result{
		UUID:  uuid.New().String(),
		Start: getTimestampMs(),
		Steps: []stepObject{},
	}
}
