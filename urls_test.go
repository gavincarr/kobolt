package kobolt

import "testing"

func TestIsKoboURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"https://www.kobo.com/au/en/ebook/foo", true},
		{"https://kobo.com/us/en/ebook/foo", true},
		{"http://www.kobo.com/gb/en/ebook/foo", true},
		{"https://store.kobo.com/au/en/ebook/foo", true},
		{"https://www.amazon.com/dp/foo", false},
		{"https://www.kobo.com.evil.com/au/en/ebook/foo", false},
		{"https://notkobo.com/au/en/ebook/foo", false},
		{"ftp://www.kobo.com/au/en/ebook/foo", false},
		{"not a url", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsKoboURL(tt.in); got != tt.want {
			t.Errorf("IsKoboURL(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
