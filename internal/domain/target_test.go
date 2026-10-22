package domain

import "testing"

func TestTargetTCP(t *testing.T) {
	tests := []struct {
		url      string
		wantTCP  bool
		wantAddr string
	}{
		{url: "tcp://db.example.com:5432", wantTCP: true, wantAddr: "db.example.com:5432"},
		{url: "tcp://[2001:db8::1]:22", wantTCP: true, wantAddr: "[2001:db8::1]:22"},
		{url: "https://api.example.com/health"},
		{url: "http://[::1"},
	}

	for _, tt := range tests {
		target := Target{URL: tt.url}

		if got := target.IsTCP(); got != tt.wantTCP {
			t.Errorf("IsTCP(%q) = %v, want %v", tt.url, got, tt.wantTCP)
		}
		if got := target.TCPAddress(); got != tt.wantAddr {
			t.Errorf("TCPAddress(%q) = %q, want %q", tt.url, got, tt.wantAddr)
		}
	}
}
