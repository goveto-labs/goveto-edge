package purge

import "testing"

func TestSameString(t *testing.T) {
	one := "one"
	otherOne := "one"
	two := "two"
	tests := []struct {
		left, right *string
		want        bool
	}{
		{want: true},
		{left: &one, right: &otherOne, want: true},
		{left: &one, right: &two},
		{left: &one},
	}
	for _, test := range tests {
		if got := sameString(test.left, test.right); got != test.want {
			t.Fatalf("sameString(%v, %v) = %v, want %v", test.left, test.right, got, test.want)
		}
	}
}
