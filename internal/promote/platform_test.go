package promote

import "testing"

func TestIPv6DNSTargets(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"95.111.234.93", ""},
		{"2a02:c207:2312:1564::1", "2a02:c207:2312:1564::1"},
		{"95.111.234.93,2a02:c207:2312:1564::1", "2a02:c207:2312:1564::1"},
		{"  2a02:c207:2312:1564::1 , 95.111.234.93 ", "2a02:c207:2312:1564::1"},
		{"::ffff:95.111.234.93", ""},
	}
	for _, tc := range tests {
		if got := IPv6DNSTargets(tc.in); got != tc.want {
			t.Fatalf("IPv6DNSTargets(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
