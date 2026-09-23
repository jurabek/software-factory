package pi

import _ "embed"

//go:embed extension.ts
var extensionSource []byte

// ExtensionSource returns the factory Pi extension that correlates factory
// requests with their native session subtrees.
func ExtensionSource() []byte { return extensionSource }
