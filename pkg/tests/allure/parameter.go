/*
The following code was adapted from https://github.com/ramich2077/allure-ginkgo/
License: No explicit license found in original repository (All Rights Reserved).
*/

package allure

const parameterReportEntryName = "PARAMETER"

type parameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
