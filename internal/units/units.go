// Package units formats the quantities quoted in the explanations: storage in decimal units (as
// drives are sold), memory in binary units (as providers publish it).
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SI formats bytes in decimal units (1 TB = 10¹² bytes).
func SI(b float64) string { return scaled(b, 1000, []string{"B", "KB", "MB", "GB", "TB", "PB"}) }

// IEC formats bytes in binary units (1 GiB = 2³⁰ bytes).
func IEC(b float64) string { return scaled(b, 1024, []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}) }

func scaled(b, base float64, names []string) string {
	i := 0
	for math.Abs(b) >= base && i < len(names)-1 {
		b /= base
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", b, names[0])
	}
	return fmt.Sprintf("%.2f %s", b, names[i])
}

// Count formats a rounded number with thousands separators: 164,000.
func Count(n float64) string { return Grouped(math.Round(n), 0) }

// Grouped formats a number to the given decimals, with thousands separators: 8,388,608.30.
func Grouped(n float64, decimals int) string {
	s := strconv.FormatFloat(math.Abs(n), 'f', decimals, 64)
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	if n < 0 && strings.Trim(s, "0.") != "" {
		b.WriteByte('-')
	}
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if frac != "" {
		b.WriteByte('.')
		b.WriteString(frac)
	}
	return b.String()
}

// Pct formats a share as a whole percentage: 0.7 → "70%".
func Pct(share float64) string { return fmt.Sprintf("%.0f%%", share*100) }

// PctTenth formats a measured share to the tenth of a point, without a trailing ".0": 0.996 →
// "99.6%", where Pct would round it to "100%".
func PctTenth(share float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", share*100), ".0") + "%"
}
