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
func Count(n float64) string {
	s := strconv.FormatFloat(math.Abs(math.Round(n)), 'f', 0, 64)
	var b strings.Builder
	if n < 0 && s != "0" {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Pct formats a share as a whole percentage: 0.7 → "70%".
func Pct(share float64) string { return fmt.Sprintf("%.0f%%", share*100) }
