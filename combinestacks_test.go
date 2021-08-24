package main

import (
	"bytes"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
				frame{"main.main", "", "/Users/ej/combinestacks/stackdemo/stackdemo.go", 25}},
		}},
		{unavailable, []routine{
			routine{"12345", "running", nil, frame{"github.com/example/golang.org/x/sync/errgroup.(*Group).Go", "",
				"###/go/src/github.com/example/golang.org/x/sync/errgroup/errgroup.go", 55}},
		}},
		{go117format, []routine{
			routine{"1", "running",
				// stack
				[]frame{
					frame{"runtime/pprof.writeGoroutineStacks", "{0x710780, 0xc000010098}", "/home/ej/go/src/runtime/pprof/pprof.go", 693},
					frame{"runtime/pprof.writeGoroutine", "{0x710780, 0xc000010098}, 0x8cc680", "/home/ej/go/src/runtime/pprof/pprof.go", 682},
				},
				// created
				frame{},
			}},
		}}

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

func TestEmptyUpload(t *testing.T) {
	s := httptest.NewServer(makeHandlers())
	defer s.Close()

	const boundary = "QQQQQboundary"
	const emptyBody = "--" + boundary + "--"
	req, err := http.NewRequest(http.MethodPost, s.URL+uploadPath, strings.NewReader(emptyBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("unexpected status", resp.Status)
	}
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	err = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(bodyBytes, []byte("must provide content")) {
		t.Error("unexpected body:", string(bodyBytes))
	}
}

func TestFileUpload(t *testing.T) {
	s := httptest.NewServer(makeHandlers())
	defer s.Close()

	// create the multipart request
	reqBuf := &bytes.Buffer{}
	reqWriter := multipart.NewWriter(reqBuf)
	w, err := reqWriter.CreateFormFile(fileFormID, "example.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	err = reqWriter.Close()
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, s.URL+uploadPath, reqBuf)
	req.Header.Set("Content-Type", reqWriter.FormDataContentType())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatal("unexpected status", resp.Status)
	}
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	err = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(bodyBytes, []byte("state=[running]")) {
		t.Error("unexpected body:", string(bodyBytes))
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

// Go 1.17 changed the format for arguments. See:
// https://go-review.googlesource.com/c/go/+/304470
// https://github.com/maruel/panicparse/issues/61
const go117format = `goroutine 1 [running]:
runtime/pprof.writeGoroutineStacks({0x710780, 0xc000010098})
        /home/ej/go/src/runtime/pprof/pprof.go:693 +0x70
runtime/pprof.writeGoroutine({0x710780, 0xc000010098}, 0x8cc680)
        /home/ej/go/src/runtime/pprof/pprof.go:682 +0x2b
`
