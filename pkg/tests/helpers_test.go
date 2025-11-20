package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRandomNamespace(t *testing.T) {
	prefix := "vm"
	ns1 := RandomNamespace(prefix)
	ns2 := RandomNamespace(prefix)

	// Check that namespaces are not empty
	assert.NotEmpty(t, ns1)
	assert.NotEmpty(t, ns2)

	// Check that namespaces are different
	assert.NotEqual(t, ns1, ns2, "RandomNamespace should generate unique namespaces")

	// Check format: prefix + "-" + 6 chars
	expectedLen := len(prefix) + 1 + 6
	assert.Len(t, ns1, expectedLen, "Namespace length should be prefix + 1 + 6")
	assert.True(t, strings.HasPrefix(ns1, prefix+"-"), "Namespace should start with prefix-")

	// Check that the suffix contains only allowed characters (lowercase latin letters)
	suffix := strings.TrimPrefix(ns1, prefix+"-")
	for _, char := range suffix {
		assert.True(t, char >= 'a' && char <= 'z', "Suffix should contain only lowercase latin letters: %c", char)
	}
}
