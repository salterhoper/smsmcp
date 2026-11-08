package phone

import "testing"

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"+255 712-345-678": "+255712345678",
		"(0712) 345 678":   "0712345678",
		"15551234":         "15551234",
	}
	for in, want := range ok {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "12", "abc123456", "+1+2345678", "1234567890123456"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) should fail", bad)
		}
	}
}

func TestSameNumber(t *testing.T) {
	if !SameNumber("+255712345678", "0712345678") {
		t.Error("local and international forms should match")
	}
	if SameNumber("+255712345678", "+255712345679") {
		t.Error("different numbers matched")
	}
}
