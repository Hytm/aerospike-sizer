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
		{Count(-0.4), "0"},
		{Grouped(8388608.3, 1), "8,388,608.3"},
		{Grouped(100.04, 2), "100.04"},
		{Grouped(-1234.5, 2), "-1,234.50"},
		{Grouped(-0.001, 2), "0.00"},
		{Grouped(1.0001e15, 0), "1,000,100,000,000,000"},
		{Pct(0.7), "70%"},
		{PctTenth(0.37), "37%"},
		{PctTenth(0.996), "99.6%"},
		{PctTenth(0.12345), "12.3%"},
		{PctTenth(0.9999), "100%"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
