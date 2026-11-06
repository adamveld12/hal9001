package daemon

import "testing"

func TestUIBaseURL(t *testing.T) {
	for _, c := range []struct{ opt, env, want string }{
		{"", "", "http://localhost:9001"},
		{"", "http://localhost:5173/", "http://localhost:5173"},
		{"http://ui.test", "http://localhost:5173", "http://ui.test"},
	} {
		if got := uiBaseURL(c.opt, c.env, 9001); got != c.want {
			t.Errorf("uiBaseURL(%q, %q) = %q, want %q", c.opt, c.env, got, c.want)
		}
	}
}
