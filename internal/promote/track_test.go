package promote

import "testing"

func TestParseTrack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		follow bool
		sha    string
	}{
		{"image: \"\"\n", true, ""},
		{"track: main\nimage: \"\"\n", true, ""},
		{"track: latest\nimage: x\n", true, ""},
		{"track: \"\"\n", true, ""},
		{"track: 8c06814474971005530a724e665ab765b69feb05\n", false, "8c06814474971005530a724e665ab765b69feb05"},
		{"track: 8C06814\n", false, "8c06814"},
	}
	for _, tc := range cases {
		got, err := ParseTrack([]byte(tc.in))
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got.FollowsMain() != tc.follow || got.SHA != tc.sha {
			t.Fatalf("%q: %+v", tc.in, got)
		}
	}
	if _, err := ParseTrack([]byte("track: :latest\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrack([]byte("track: not-a-sha\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestTrackMatchesCommit(t *testing.T) {
	t.Parallel()
	full := "8c06814474971005530a724e665ab765b69feb05"
	pin := Track{SHA: "8c06814"}
	if !pin.MatchesCommit(full) || !pin.MatchesCommit("8c06814") {
		t.Fatal("short pin should match")
	}
	if pin.MatchesCommit("f10cf296ea2c210d374847d1368d0ef9c848664c") {
		t.Fatal("other commit")
	}
	if !(Track{Follow: true}).MatchesCommit(full) {
		t.Fatal("main follows any push")
	}
}
