package lifecycle

import (
	"math"
	"testing"
)

// The expected bytes are the output of the Python reference codec
// (src/cel-lifecycle/git_store.py encode(decode(input))), recorded once.
func TestCodecMatchesTheReferenceCodec(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"b":1,"a":[true,false,null]}`, "{\"a\":[true,false,null],\"b\":1}\n"},
		{`[1.0,1e16,1e15,0.0001,0.00001,-0.0,1.5e-7,123456789012345680.0,2.5,1E2,-0,1e-400]`,
			"[1.0,1e+16,1000000000000000.0,0.0001,1e-05,-0.0,1.5e-07,1.2345678901234568e+17,2.5,100.0,0,0.0]\n"},
		{`"é <>&\u0001\u001f\u007f😀\b\f\n\r\t\/"`,
			"\"\xc3\xa9\xe2\x80\xa8<>&\\u0001\\u001f\x7f\xf0\x9f\x98\x80\\b\\f\\n\\r\\t/\"\n"},
		{`{"é":1,"z":2,"Z":3,"😀":4}`, "{\"Z\":3,\"z\":2,\"\xc3\xa9\":1,\"\xf0\x9f\x98\x80\":4}\n"},
		{"  [ ]  ", "[]\n"},
		{"123456789012345678901234567890", "123456789012345678901234567890\n"},
	}
	for _, c := range cases {
		value, err := Decode([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got := string(Encode(value)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCodecRefusesWhatTheReferenceRefuses(t *testing.T) {
	bad := []string{`{"a":1,"a":2}`, `[NaN]`, `[Infinity]`, `"\ud800"`, `"\udc00"`, `[1e400]`, `01`, `[1.]`,
		"\"a\x01\"", "\xef\xbb\xbf[]", `[1] x`, "\xff"}
	for _, raw := range bad {
		_, err := Decode([]byte(raw))
		fault, ok := err.(*Fault)
		if !ok || fault.Code != "invalid_record" || fault.Stage != "codec" {
			t.Errorf("%q: got %v, want an invalid_record codec fault", raw, err)
		}
	}
	deep := make([]byte, 0, 800000)
	for i := 0; i < 400000; i++ {
		deep = append(deep, '[')
	}
	for i := 0; i < 400000; i++ {
		deep = append(deep, ']')
	}
	if _, err := Decode(deep); err == nil || err.(*Fault).Code != "invalid_record" {
		t.Errorf("deep nesting: got %v", err)
	}
}

func TestEqualityFollowsTheReference(t *testing.T) {
	one, _ := Decode([]byte("1"))
	oneFloat, _ := Decode([]byte("1.0"))
	if !equal(one, oneFloat) || !equal(true, one) || equal("1", one) || equal(nil, false) {
		t.Fatal("numeric equality differs from the reference")
	}
	a, _ := Decode([]byte(`{"x":[1,{"y":true}]}`))
	b, _ := Decode([]byte(`{"x":[1.0,{"y":1}]}`))
	if !equal(a, b) {
		t.Fatal("nested equality differs from the reference")
	}
}

func TestEqualityOfNonFiniteFloatsAndStringLists(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	if equal(nan, nan) || equal(nan, Int{"1"}) || equal(Int{"1"}, inf) || !equal(inf, inf) || equal(inf, math.Inf(-1)) {
		t.Fatal("non-finite floats differ from the Python rules")
	}
	if !equal([]string{"a", "b"}, Array{"a", "b"}) || !equal(Array{"a"}, []string{"a"}) || equal([]string{"a"}, Array{"b"}) {
		t.Fatal("a []string must equal the Array with the same items")
	}
}
