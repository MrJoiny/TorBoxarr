package files_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/mrjoiny/torboxarr/internal/files"
)

func TestProbablyObfuscated(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"f0RrkVAuC2Fy6Lld8IL4CaHAPMp7bJ65", true},         // random letters and digits
		{"1bd7848ddcaf43018d8b46433a8f7fcf", true},         // md5
		{"a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4", true}, // long hex
		{"CIA.2026.S01E10.720p.10bit.WEBRip.2CH.x265.HEVC-PSA", false},
		{"Your Honor S02E01", false},
		{"movie", false},
		{"Interstellar2014", false}, // too few digits for a random mix
		{"sample", false},
	}
	for _, tt := range tests {
		if got := files.ProbablyObfuscated(tt.name); got != tt.want {
			t.Errorf("ProbablyObfuscated(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestDeobfuscate_RenamesMainFileAndCompanions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f0RrkVAuC2Fy6Lld8IL4CaHAPMp7bJ65.mkv"), 1000)
	writeFile(t, filepath.Join(dir, "f0RrkVAuC2Fy6Lld8IL4CaHAPMp7bJ65.en.srt"), 10)
	writeFile(t, filepath.Join(dir, "readme.nfo"), 5)

	n, err := files.Deobfuscate(dir, "CIA.2026.S01E10.720p.10bit.WEBRip.2CH.x265.HEVC-PSA")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("renamed %d files, want 2", n)
	}
	want := []string{
		"CIA.2026.S01E10.720p.10bit.WEBRip.2CH.x265.HEVC-PSA.en.srt",
		"CIA.2026.S01E10.720p.10bit.WEBRip.2CH.x265.HEVC-PSA.mkv",
		"readme.nfo",
	}
	if got := listNames(t, dir); !equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestDeobfuscate_LeavesReleaseNamesAlone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Show.S01E01.720p.WEB.x265-GRP.mkv"), 1000)

	n, err := files.Deobfuscate(dir, "Something Else")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("renamed %d files, want 0", n)
	}
	if got := listNames(t, dir); !equal(got, []string{"Show.S01E01.720p.WEB.x265-GRP.mkv"}) {
		t.Errorf("files = %v", got)
	}
}

func TestDeobfuscate_FindsFileInSubfolderAndStripsNZBSuffix(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sub", "1bd7848ddcaf43018d8b46433a8f7fcf.mkv"), 1000)

	if _, err := files.Deobfuscate(dir, "Your.Honor.US.S02E09.720p.WEBRip.x265-MiNX.nzb"); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, filepath.Join(dir, "sub")); !equal(got, []string{"Your.Honor.US.S02E09.720p.WEBRip.x265-MiNX.mkv"}) {
		t.Errorf("files = %v", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
