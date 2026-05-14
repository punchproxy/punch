package source

import "testing"

func TestIsSourceRecognizesURLsAndPaths(t *testing.T) {
	for _, entry := range []string{
		"https://example.com/cidr.txt",
		"http://example.com/cidr.txt",
		"/etc/punch/routes.txt",
		`C:\Temp\routes.txt`,
		`C:/Temp/routes.txt`,
		`\\server\share\routes.txt`,
		"./routes.txt",
		`.\routes.txt`,
		"../routes.txt",
		`..\routes.txt`,
		"~/routes.txt",
	} {
		if !IsSource(entry) {
			t.Fatalf("IsSource(%q) = false, want true", entry)
		}
	}
}

func TestIsSourceLeavesInlineCIDRsAlone(t *testing.T) {
	for _, entry := range []string{
		"10.0.0.0/8",
		"2001:b28:f23d::/48",
		"domain:example.com",
		"keyword:google",
		"a:b",
	} {
		if IsSource(entry) {
			t.Fatalf("IsSource(%q) = true, want false", entry)
		}
	}
}
