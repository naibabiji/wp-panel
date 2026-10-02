package handlers

import "testing"

func TestParseCPUTicksSeparatesIOWaitAndSteal(t *testing.T) {
	ticks := parseCPUTicks("cpu  100 10 20 300 40 5 6 7 80 9\n")
	if ticks.total != 488 || ticks.idle != 300 || ticks.iowait != 40 || ticks.steal != 7 {
		t.Fatalf("ticks=%+v", ticks)
	}
}
