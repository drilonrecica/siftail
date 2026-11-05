//go:build !linux

package retention

func testFilesystem(string) string { return "unknown" }
