package source

import "strings"

// IsSource reports whether entry identifies a URL or local file source rather
// than an inline rule or CIDR.
func IsSource(entry string) bool {
	return IsRemote(entry) || IsLocalPath(entry)
}

func IsRemote(entry string) bool {
	entry = strings.TrimSpace(entry)
	return strings.HasPrefix(entry, "http://") || strings.HasPrefix(entry, "https://")
}

func IsLocalPath(entry string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	return strings.HasPrefix(entry, "/") ||
		strings.HasPrefix(entry, `\`) ||
		strings.HasPrefix(entry, "./") ||
		strings.HasPrefix(entry, `.\`) ||
		strings.HasPrefix(entry, "../") ||
		strings.HasPrefix(entry, `..\`) ||
		strings.HasPrefix(entry, "~/") ||
		isWindowsDrivePath(entry)
}

func isWindowsDrivePath(entry string) bool {
	if len(entry) < 3 || entry[1] != ':' || !isASCIILetter(entry[0]) {
		return false
	}
	return entry[2] == '/' || entry[2] == '\\'
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
