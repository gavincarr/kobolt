package main

import "testing"

func TestMungeUA(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{
			name: "headless",
			in:   "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/158.0.0.0 Safari/537.36",
			want: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36",
		},
		{
			name: "already regular",
			in:   "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36",
			want: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36",
		},
		{name: "empty", in: "", wantErr: true},
		{name: "no chrome token", in: "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0", wantErr: true},
		{name: "other headless marker", in: "Mozilla/5.0 Headless Chrome/158.0.0.0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mungeUA(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("mungeUA(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("mungeUA(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
