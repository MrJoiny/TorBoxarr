package files

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var (
	md5Name     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	longHexName = regexp.MustCompile(`^[a-f0-9.]{40,}$`)
)

// ProbablyObfuscated reports whether a file name (without extension) looks
// like a usenet poster's random name rather than a release name. It is
// stricter than SABnzbd's check, which calls anything it cannot place
// obfuscated: only hex digests and long random mixes of letters and digits
// with no separators count here.
func ProbablyObfuscated(base string) bool {
	if md5Name.MatchString(base) || longHexName.MatchString(base) {
		return true
	}
	var digits, upper, lower, separators int
	for _, r := range base {
		switch {
		case unicode.IsDigit(r):
			digits++
		case unicode.IsUpper(r):
			upper++
		case unicode.IsLower(r):
			lower++
		case r == ' ' || r == '.' || r == '_' || r == '-':
			separators++
		}
	}
	return len(base) >= 16 && separators == 0 && digits >= 3 && upper >= 2 && lower >= 2
}

// Deobfuscate gives an NZB job's main file the job's name when the poster
// obfuscated it, like SABnzbd's "deobfuscate final filenames". The main file
// is the largest one under dir; files beside it with the same base name (such
// as subtitles) are renamed with it. It returns how many files it renamed.
func Deobfuscate(dir, jobName string) (int, error) {
	var largest string
	var largestSize int64 = -1
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > largestSize {
			largest, largestSize = path, info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scan %s: %w", dir, err)
	}
	if largest == "" {
		return 0, nil
	}
	oldBase := strings.TrimSuffix(filepath.Base(largest), filepath.Ext(largest))
	if !ProbablyObfuscated(oldBase) {
		return 0, nil
	}
	newBase := sanitize(strings.TrimSuffix(strings.TrimSpace(jobName), ".nzb"))
	if newBase == "" || newBase == "unnamed" {
		return 0, nil
	}

	folder := filepath.Dir(largest)
	entries, err := os.ReadDir(folder)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", folder, err)
	}
	renamed := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, oldBase) {
			continue
		}
		rest := name[len(oldBase):]
		if rest != "" && !strings.HasPrefix(rest, ".") {
			continue
		}
		target := filepath.Join(folder, newBase+rest)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := os.Rename(filepath.Join(folder, name), target); err != nil {
			return renamed, fmt.Errorf("rename %s: %w", name, err)
		}
		renamed++
	}
	return renamed, nil
}
