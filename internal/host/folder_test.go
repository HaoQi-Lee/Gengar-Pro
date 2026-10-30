package host

import "testing"

func TestNormalizeFolder(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"  ", ""},
		{"生产/web", "生产/web"},
		{"/生产/web/", "生产/web"},
		{"//生产//web//", "生产/web"},
		{"生产\\web", "生产/web"},
		{" a / b /c ", "a/b/c"},
		{"///", ""},
	}
	for _, c := range cases {
		if got := NormalizeFolder(c.in); got != c.want {
			t.Errorf("NormalizeFolder(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
