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

const unavailable = `
extra: goroutine 12345 [running]:
extra: ###goroutine running on other thread; stack unavailable
extra: created by github.com/example/golang.org/x/sync/errgroup.(*Group).Go
extra: ###/go/src/github.com/example/golang.org/x/sync/errgroup/errgroup.go:55 +0xab
`
