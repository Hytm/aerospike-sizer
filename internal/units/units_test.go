package units

import "testing"

func TestFormats(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{SI(1.464e12), "1.46 TB"},
		{SI(0), "0 B"},
		{SI(512), "512 B"},
		{IEC(128 << 30), "128.00 GiB"},
		{Count(164000), "164,000"},
		{Count(999), "999"},
		{Count(-1234567.4), "-1,234,567"},
		{Pct(0.7), "70%"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
