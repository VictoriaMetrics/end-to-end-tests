/*
The following code was adapted from https://github.com/ramich2077/allure-ginkgo/
License: No explicit license found in original repository (All Rights Reserved).
*/

package allure

import (
	"fmt"
)

// logEntry represents a single log message associated with a step.
// Maps to the Allure 2 logEntries field.
type logEntry struct {
	Name      string `json:"name"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

type stepObject struct {
	Name          string         `json:"name,omitempty"`
	Status        string         `json:"status,omitempty"`
	Description   string         `json:"description,omitempty"`
	StatusDetails *statusDetails `json:"statusDetails,omitempty"`
	Stage         string         `json:"stage"`
	ChildrenSteps []stepObject   `json:"steps"`
	Attachments   []attachment   `json:"attachments"`
	LogEntries    []logEntry     `json:"logEntries"`
	Start         int64          `json:"start"`
	Stop          int64          `json:"stop"`
}

func (sc *stepObject) addName(name string) {
	sc.Name = name
}

func (sc *stepObject) addAttachment(attachment *attachment) {
	if attachment == nil {
		panic(fmt.Errorf("nil attachment pointer"))
	}
	sc.Attachments = append(sc.Attachments, *attachment)
}

func (sc *stepObject) addLogEntry(entry logEntry) {
	sc.LogEntries = append(sc.LogEntries, entry)
}

func newStep() *stepObject {
	return &stepObject{
		Attachments:   make([]attachment, 0),
		ChildrenSteps: make([]stepObject, 0),
		LogEntries:    make([]logEntry, 0),
	}
}
