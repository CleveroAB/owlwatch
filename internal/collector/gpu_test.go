package collector

import (
	"reflect"
	"testing"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

func TestParseNvidiaSMI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []metrics.GPUMetrics
	}{
		{
			name: "single GPU",
			in:   "0, NVIDIA GeForce RTX 4090, 62, 24564, 4103, 61, 128.50\n",
			want: []metrics.GPUMetrics{{
				Index:    0,
				Name:     "NVIDIA GeForce RTX 4090",
				UtilPct:  62,
				MemTotal: 24564 * mib,
				MemUsed:  4103 * mib,
				TempC:    61,
				PowerW:   128.5,
			}},
		},
		{
			name: "multiple GPUs",
			in:   "0, Tesla T4, 10, 15360, 512, 40, 28.1\n1, Tesla T4, 95, 15360, 14000, 78, 69.9\n",
			want: []metrics.GPUMetrics{
				{Index: 0, Name: "Tesla T4", UtilPct: 10, MemTotal: 15360 * mib, MemUsed: 512 * mib, TempC: 40, PowerW: 28.1},
				{Index: 1, Name: "Tesla T4", UtilPct: 95, MemTotal: 15360 * mib, MemUsed: 14000 * mib, TempC: 78, PowerW: 69.9},
			},
		},
		{
			name: "not-supported and N/A fields become zero",
			in:   "0, Tesla K80, [N/A], 11441, [Not Supported], 45, [N/A]\n",
			want: []metrics.GPUMetrics{{
				Index:    0,
				Name:     "Tesla K80",
				UtilPct:  0,
				MemTotal: 11441 * mib,
				MemUsed:  0,
				TempC:    45,
				PowerW:   0,
			}},
		},
		{
			name: "GPU name containing a comma",
			in:   "0, NVIDIA H100 80GB HBM3, rev 2, 12, 81559, 100, 30, 350\n",
			want: []metrics.GPUMetrics{{
				Index:    0,
				Name:     "NVIDIA H100 80GB HBM3, rev 2",
				UtilPct:  12,
				MemTotal: 81559 * mib,
				MemUsed:  100 * mib,
				TempC:    30,
				PowerW:   350,
			}},
		},
		{
			name: "malformed line skipped, good lines kept",
			in:   "garbage line\n0, Tesla T4, 10, 15360, 512, 40, 28.1\n\n",
			want: []metrics.GPUMetrics{
				{Index: 0, Name: "Tesla T4", UtilPct: 10, MemTotal: 15360 * mib, MemUsed: 512 * mib, TempC: 40, PowerW: 28.1},
			},
		},
		{
			name: "CRLF line endings",
			in:   "0, Tesla T4, 10, 15360, 512, 40, 28.1\r\n",
			want: []metrics.GPUMetrics{
				{Index: 0, Name: "Tesla T4", UtilPct: 10, MemTotal: 15360 * mib, MemUsed: 512 * mib, TempC: 40, PowerW: 28.1},
			},
		},
		{
			name: "unparseable index falls back to line position",
			in:   "[N/A], Tesla T4, 10, 15360, 512, 40, 28.1\n",
			want: []metrics.GPUMetrics{
				{Index: 0, Name: "Tesla T4", UtilPct: 10, MemTotal: 15360 * mib, MemUsed: 512 * mib, TempC: 40, PowerW: 28.1},
			},
		},
		{
			name: "empty output",
			in:   "",
			want: []metrics.GPUMetrics{},
		},
		{
			name: "whitespace-only output",
			in:   "\n  \n",
			want: []metrics.GPUMetrics{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNvidiaSMI([]byte(tt.in))
			if got == nil {
				t.Fatal("parseNvidiaSMI returned nil, want non-nil slice")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNvidiaSMI:\n got  %+v\n want %+v", got, tt.want)
			}
		})
	}
}

func TestSMIFloat(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"62", 62},
		{" 128.50 ", 128.5},
		{"[N/A]", 0},
		{"[Not Supported]", 0},
		{"N/A", 0},
		{"", 0},
		{"-5", 0}, // negatives would corrupt the uint64 conversions
	}
	for _, tt := range tests {
		if got := smiFloat(tt.in); got != tt.want {
			t.Errorf("smiFloat(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
