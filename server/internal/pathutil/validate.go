package pathutil

import (
	"fmt"
	"path"
	"strings"
)

// ValidatePath rejects paths that escape the virtual root:
//   - must start with "/"
//   - must not contain ".." components after cleaning
//   - must not be empty
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("path must not be empty")
	}
	clean := path.Clean(p)
	if !strings.HasPrefix(clean, "/") {
		return fmt.Errorf("path must be absolute: %q", p)
	}
	// path.Clean resolves ".." — if the result differs from a naive join it
	// means traversal was attempted.
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return fmt.Errorf("path traversal not allowed: %q", p)
		}
	}
	return nil
}