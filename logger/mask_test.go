package logger

import "testing"

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":                         "",
		"short":                    "****",
		"8626379321:AAE5qjq5ol-dp": "8626****l-dp",
	}
	for in, want := range cases {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskDSN(t *testing.T) {
	cases := map[string]string{
		"./data/muse_bot.db":                     "./data/muse_bot.db",
		"root:p@ss@tcp(127.0.0.1:3306)/muse?a=b": "root:****@tcp(127.0.0.1:3306)/muse?a=b",
	}
	for in, want := range cases {
		if got := MaskDSN(in); got != want {
			t.Errorf("MaskDSN(%q) = %q, want %q", in, got, want)
		}
	}
}
