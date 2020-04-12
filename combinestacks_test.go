package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	type test struct {
		input    string
		expected []routine
	}

	testCases := []test{
		{plain, []routine{
			routine{"1", "running",
				// stack
				[]frame{
					frame{"main.main.func1", "0xc000194000", "/Users/ej/combinestacks/stackdemo/stackdemo.go", 26},
				},
				// created
				frame{"main.main", "",
					"/Users/ej/combinestacks/stackdemo/stackdemo.go", 25}},
		}},
		{unavailable, []routine{
			routine{"12345", "running", nil, frame{"github.com/example/golang.org/x/sync/errgroup.(*Group).Go", "",
				"###/go/src/github.com/example/golang.org/x/sync/errgroup/errgroup.go", 55}},
		}},
	}

	for i, testCase := range testCases {
		output, err := parse(strings.NewReader(testCase.input))
		if err != nil {
			t.Errorf("%d: failed to parse: %s", i, err.Error())
			continue
		}
		if !reflect.DeepEqual(output, testCase.expected) {
			t.Errorf("%d failed; EXPECTED:\n", i)
			t.Errorf("%#v", testCase.expected)
			t.Error("ACTUAL:")
			t.Errorf("%#v", output)
		}
	}
}

const plain = `goroutine 1 [running]:
main.main.func1(0xc000194000)
	/Users/ej/combinestacks/stackdemo/stackdemo.go:26 +0x76
created by main.main
	/Users/ej/combinestacks/stackdemo/stackdemo.go:25 +0x647
`

const unavailable = `
extra: goroutine 12345 [running]:
extra: ###goroutine running on other thread; stack unavailable
extra: created by github.com/example/golang.org/x/sync/errgroup.(*Group).Go
extra: ###/go/src/github.com/example/golang.org/x/sync/errgroup/errgroup.go:55 +0xab
`
