package tui

import (
	"fmt"
	"strings"

	"github.com/nartkosova/tui-load-testing-tool/internal/load"
)

const histogramHeight = 6

// histogram draws the latency buckets as vertical bars, each three cells
// wide, with eighth-block characters for the partial top of a bar.
func histogram(stats load.LatencyStats) string {
	title := dimStyle.Render("latency distribution (min → p99)")
	if !stats.Available() || len(stats.Buckets) == 0 {
		return title + "\n" + dimStyle.Render("no successful responses yet")
	}
	var peak int64
	for _, b := range stats.Buckets {
		peak = max(peak, b.Count)
	}
	rows := make([]string, histogramHeight)
	for row := range histogramHeight {
		var line strings.Builder
		floor := float64(histogramHeight-1-row) * 8
		for _, b := range stats.Buckets {
			eighths := float64(b.Count) / float64(peak) * histogramHeight * 8
			cell := " "
			switch {
			case eighths >= floor+8:
				cell = "█"
			case eighths > floor:
				cell = string(sparkLevels[int(eighths-floor)])
			case row == histogramHeight-1 && b.Count > 0:
				cell = "▁"
			}
			line.WriteString(strings.Repeat(cell, 2) + " ")
		}
		rows[row] = sparkStyle.Render(line.String())
	}
	width := len(stats.Buckets) * 3
	low, high := short(stats.Buckets[0].From), short(stats.Buckets[len(stats.Buckets)-1].To)
	axis := dimStyle.Render(low + strings.Repeat(" ", max(1, width-len(low)-len(high)-1)) + high)
	return fmt.Sprintf("%s  %s\n%s\n%s", title, dimStyle.Render(fmt.Sprintf("peak %s", count(peak))), strings.Join(rows, "\n"), axis)
}
