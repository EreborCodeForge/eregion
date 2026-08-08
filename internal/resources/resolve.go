package resources

import (
	"path"
	"strings"
)

// ResolveCgroupPath joins rawPath under root and rejects escapes outside root.
// Empty or "/" rawPath resolves to root itself.
func ResolveCgroupPath(root, rawPath string) (resolved string, ok bool) {
	root = path.Clean(root)
	if root == "" || root == "." {
		return "", false
	}

	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" || rawPath == "/" {
		return root, true
	}

	// Reject parent-directory segments before Clean so escape attempts cannot
	// be rewritten into an in-root relative path (e.g. ../../etc → etc).
	if strings.Contains(rawPath, "..") {
		return "", false
	}

	clean := path.Clean("/" + rawPath)
	relative := strings.TrimPrefix(clean, "/")
	if relative == "" || relative == "." {
		return root, true
	}

	resolved = path.Clean(path.Join(root, relative))
	if resolved == root {
		return root, true
	}
	if !strings.HasPrefix(resolved, root+"/") {
		return "", false
	}
	return resolved, true
}
