package tester

import (
	"net"
	"reflect"
	"testing"
)

func TestIterateIPsVisitsEveryDisjointRange(t *testing.T) {
	cases := []struct {
		name, input string
		want        []string
	}{
		{"reported six sources", "1.1.1.1\n8.8.8.8\n2.189.68.126\n46.245.51.113\n185.166.107.100\n185.143.235.62", []string{"1.1.1.1", "2.189.68.126", "8.8.8.8", "46.245.51.113", "185.143.235.62", "185.166.107.100"}},
		{"mixed ranges and final IPv4", "192.0.2.1-192.0.2.2\n198.51.100.4/31\n255.255.255.255", []string{"192.0.2.1", "192.0.2.2", "198.51.100.4", "198.51.100.5", "255.255.255.255"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ranges, err := ParseIPRangesFromString(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			ranges.IterateIPs(func(ip net.IP) bool { got = append(got, ip.String()); return true })
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("visited %v; want %v", got, tc.want)
			}
			if int64(len(got)) != ranges.Total() {
				t.Fatalf("visited %d of %d", len(got), ranges.Total())
			}
		})
	}
}

func TestIterateIPsCancellationStopsAcrossRanges(t *testing.T) {
	ranges, _ := ParseIPRangesFromString("192.0.2.1\n198.51.100.1-198.51.100.3")
	count := 0
	ranges.IterateIPs(func(net.IP) bool { count++; return count < 2 })
	if count != 2 {
		t.Fatalf("cancellation visited %d sources", count)
	}
}
