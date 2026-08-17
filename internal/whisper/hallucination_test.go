package whisper

import "testing"

func TestScrubHallucinationDropsWhole(t *testing.T) {
	cases := []string{
		"Продолжение следует...",
		"продолжение следует.",
		"Thanks for watching!",
		"Спасибо за просмотр",
	}
	for _, c := range cases {
		if got := ScrubHallucination(c); got != "" {
			t.Errorf("ScrubHallucination(%q)=%q want empty", c, got)
		}
	}
}

func TestScrubHallucinationKeepsRealSpeech(t *testing.T) {
	s := "Проверяю работу микрофона."
	if got := ScrubHallucination(s); got != s {
		t.Fatalf("got %q want %q", got, s)
	}
}

func TestScrubHallucinationStripsTrailing(t *testing.T) {
	in := "Проверяю работу микрофона. Продолжение следует..."
	want := "Проверяю работу микрофона."
	if got := ScrubHallucination(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
