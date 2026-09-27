package api

import "testing"

func TestMagnetName(t *testing.T) {
	cases := map[string]string{
		"magnet:?xt=urn:btih:ABC&dn=Conan+O'Brien+Must+Go+S02+%5bGRP%5d": "Conan O'Brien Must Go S02 [GRP]",
		"magnet:?xt=urn:btih:ABC":               "",
		"https://example.com/file.torrent?dn=x": "",
		"not a url %zz":                         "",
	}
	for in, want := range cases {
		if got := magnetName(in); got != want {
			t.Errorf("magnetName(%q) = %q, want %q", in, got, want)
		}
	}
}
