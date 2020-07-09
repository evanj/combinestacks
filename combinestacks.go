package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/evanj/combinestacks/forked/panicparse/exportpanicparse"
)

const portEnvVar = "PORT"
const uploadPath = "/upload"
const panicParsePath = "/panicparse"
const textFormID = "text"
const fileFormID = "file"
const maxFormMemoryBytes = 32 * 1024 * 1024

type frame struct {
	function string
	args     string
	file     string
	line     int
}

type routine struct {
	label   string
	state   string
	stack   []frame
	created frame
}

var startRuntimeStack = regexp.MustCompile(`(runtime stack):`)

const runtimeState = "running"

// example: goroutine 12345 [running]:
var goroutineStart = regexp.MustCompile(`goroutine (\d+) \[([^\]]+)\]`)

var callLine = regexp.MustCompile(`([^ ]+)\(([^)]*)\)\s*$`)

var createdByLine = regexp.MustCompile(`created by ([^ ]+)`)

// .go stacks end with +0x66
// .s stacks end with fp=0xcb8de6e140 sp=0xcb8de6e138 pc=0x475e60
// we just ignore anything after the file/line number
var fileLine = regexp.MustCompile(`([^\s]+):(\d+)($| \+| fp=).*$`)

func parse(r io.Reader) ([]routine, error) {
	parsedRoutines := []routine{}
	scanner := bufio.NewScanner(r)

	createdByFound := false
	for scanner.Scan() {
		matches := startRuntimeStack.FindSubmatch(scanner.Bytes())
		if len(matches) > 0 {
			r := routine{string(matches[1]), runtimeState, nil, frame{}}
			parsedRoutines = append(parsedRoutines, r)
			createdByFound = false
			continue
		}

		matches = goroutineStart.FindSubmatch(scanner.Bytes())
		if len(matches) > 0 {
			r := routine{string(matches[1]), string(matches[2]), nil, frame{}}
			parsedRoutines = append(parsedRoutines, r)
			createdByFound = false
			continue
		}

		matches = callLine.FindSubmatch(scanner.Bytes())
		if len(matches) > 0 {
			if len(parsedRoutines) == 0 {
				return nil, errors.New("found call line without a routine: " + scanner.Text())
			}

			f := frame{string(matches[1]), string(matches[2]), "", 0}
			parsedRoutines[len(parsedRoutines)-1].stack = append(parsedRoutines[len(parsedRoutines)-1].stack, f)
			continue
		}

		matches = createdByLine.FindSubmatch(scanner.Bytes())
		if len(matches) > 0 {
			if len(parsedRoutines) == 0 {
				return nil, errors.New("found created by line without a routine: " + scanner.Text())
			}

			f := frame{string(matches[1]), "", "", 0}
			parsedRoutines[len(parsedRoutines)-1].created = f
			createdByFound = true
			continue
		}

		matches = fileLine.FindSubmatch(scanner.Bytes())
		if len(matches) > 0 {
			line, err := strconv.ParseInt(string(matches[2]), 10, 0)
			if err != nil {
				return nil, err
			}

			if len(parsedRoutines) == 0 {
				return nil, errors.New("found file line without a routine: " + scanner.Text())
			}
			lastRoutine := parsedRoutines[len(parsedRoutines)-1]

			if createdByFound {
				lastRoutine.created.file = string(matches[1])
				lastRoutine.created.line = int(line)
				createdByFound = false
			} else {
				if len(lastRoutine.stack) == 0 {
					return nil, errors.New("found file line without a frame: " + scanner.Text())
				}

				lastRoutine.stack[len(lastRoutine.stack)-1].file = string(matches[1])
				lastRoutine.stack[len(lastRoutine.stack)-1].line = int(line)
			}
			parsedRoutines[len(parsedRoutines)-1] = lastRoutine
			continue
		}
	}
	if scanner.Err() != nil {
		return nil, scanner.Err()
	}
	return parsedRoutines, nil
}

func print(routines []routine) {
	for i, routine := range routines {
		fmt.Printf("%d %s [%s]\n", i, routine.label, routine.state)
		for j, f := range routine.stack {
			fmt.Printf("  %2d: %s(%s)\n", j, f.function, f.args)
			fmt.Printf("        %s:%d\n", f.file, f.line)
		}
		if routine.created.function != "" {
			fmt.Printf("  created by %s\n", routine.created.function)
			fmt.Printf("        %s:%d\n", routine.created.file, routine.created.line)
		}
	}
}

type stackHash [sha256.Size]byte

func (s stackHash) String() string {
	return hex.EncodeToString(s[0:len(s)])
}

func hashFrame(w io.Writer, f frame) {
	w.Write([]byte(f.function))
	w.Write([]byte("|"))
	w.Write([]byte(f.file))
	w.Write([]byte("|"))
	w.Write([]byte(strconv.Itoa(f.line)))
	w.Write([]byte("|"))
}

func hash(r routine) stackHash {
	hasher := sha256.New()
	for _, frame := range r.stack {
		hashFrame(hasher, frame)
	}
	hashFrame(hasher, r.created)

	// copy the hash to the output array
	var output stackHash
	slice := output[0:0:len(output)]
	hasher.Sum(slice)
	return output
}

// writeAggregated writes aggregated stacks to w.
func writeAggregated(w io.Writer, routines []routine) error {
	groups := map[stackHash][]routine{}
	for _, r := range routines {
		h := hash(r)
		groups[h] = append(groups[h], r)
	}

	// sort the group keys in descending order (largest groups to smallest)
	sortedGroupHashes := make([]stackHash, 0, len(groups))
	for h := range groups {
		sortedGroupHashes = append(sortedGroupHashes, h)
	}
	sort.Slice(sortedGroupHashes, func(i int, j int) bool {
		iGroups := groups[sortedGroupHashes[i]]
		jGroups := groups[sortedGroupHashes[j]]
		return len(iGroups) > len(jGroups)
	})

	fmt.Fprintf(w, "Found %d total goroutines\n", len(routines))
	for _, h := range sortedGroupHashes {
		group := groups[h]
		fmt.Fprintf(w, "\n%d goroutines; example goroutine=%s; state=[%s]\n",
			len(group), group[0].label, group[0].state)

		for _, f := range group[0].stack {
			fmt.Fprintf(w, "%s(%s)\n", f.function, f.args)
			fmt.Fprintf(w, "\t%s:%d\n", f.file, f.line)
		}
		if group[0].created.function != "" {
			fmt.Fprintf(w, "created by %s\n", group[0].created.function)
			fmt.Fprintf(w, "\t%s:%d\n", group[0].created.file, group[0].created.line)
		}
	}
	return nil
}

var errMissing = errors.New("combinestacks: missing stack text")

func getStackText(r *http.Request) (string, error) {
	err := r.ParseMultipartForm(maxFormMemoryBytes)
	if err != nil {
		return "", err
	}

	// try the form field first then fall back to file upload
	v := r.FormValue(textFormID)
	if v != "" {
		return v, nil
	}

	mpf, _, err := r.FormFile(fileFormID)
	if err == http.ErrMissingFile {
		return "", errMissing
	}
	if err != nil {
		return "", err
	}
	fBytes, err := ioutil.ReadAll(mpf)
	if err != nil {
		return "", err
	}
	err = mpf.Close()
	if err != nil {
		return "", err
	}
	v = string(fBytes)
	if v == "" {
		return "", errMissing
	}
	return v, nil
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleUpload %s %s", r.Method, r.URL.String())
	if r.Method != http.MethodPost {
		http.Error(w, "wrong method", http.StatusMethodNotAllowed)
		return
	}
	v, err := getStackText(r)
	if err != nil {
		if err == errMissing {
			http.Error(w, "must provide content", http.StatusBadRequest)
			return
		}
		panic(err)
	}

	routines, err := parse(strings.NewReader(v))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	err = writeAggregated(w, routines)
	if err != nil {
		panic(err)
	}
}

func handlePanicParse(w http.ResponseWriter, r *http.Request) {
	log.Printf("handlePanicParse %s %s", r.Method, r.URL.String())
	if r.Method != http.MethodPost {
		http.Error(w, "wrong method", http.StatusMethodNotAllowed)
		return
	}
	v, err := getStackText(r)
	if err != nil {
		if err == errMissing {
			http.Error(w, "must provide content", http.StatusBadRequest)
			return
		}
		panic(err)
	}

	w.Header().Set("Content-Type", "text/html;charset=utf-8")
	err = exportpanicparse.ProcessHTML(strings.NewReader(v), w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleRoot %s %s", r.Method, r.URL.String())
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "invalid method", http.StatusMethodNotAllowed)
		return
	}

	_, err := w.Write([]byte(rootTemplate))
	if err != nil {
		panic(err)
	}
}

const rootTemplate = `<!doctype html>
<html>
<head><title>Combine Go Stacks</title></head>
<body>
<h1>Combine Go Stacks</h1>
<p>Paste Go stack traces in text format, and get a report with the same traces combined. This is useful for figuring out what a big program was doing when it crashed. See <a href="https://www.evanjones.ca/go-stace-traces.html">my blog post for details</a>. The code powering this is <a href="https://github.com/maruel/panicparse">panicparse</a>, which is a great tool!</p>

<form method="post" action="` + panicParsePath + `" enctype="multipart/form-data">
<textarea name="` + textFormID + `" rows="10" cols="120" wrap="off" autofocus></textarea>
<p>Alternative file upload: <input type="file" name="` + fileFormID + `"></p>
<p><input type="submit" value="Panic Parse"> <input type="submit" value="Hacky Parser (might collapse more)" formaction="` + uploadPath + `"></p>
</form>

<h2>Example Input</h2>
<pre>
goroutine 182 [semacquire]:
main.b2(0xc000192068)
  /Users/ej/combinestacks/stackdemo/stackdemo.go:86 +0x66
main.b1(0xc000192068)
  /Users/ej/combinestacks/stackdemo/stackdemo.go:82 +0x2b
created by main.main
  /Users/ej/combinestacks/stackdemo/stackdemo.go:41 +0x367

goroutine 182 [semacquire]:
main.b2(0xc000192068)
  /Users/ej/combinestacks/stackdemo/stackdemo.go:86 +0x66
main.b1(0xc000192068)
  /Users/ej/combinestacks/stackdemo/stackdemo.go:82 +0x2b
created by main.main
  /Users/ej/combinestacks/stackdemo/stackdemo.go:41 +0x367
</pre>

<h2>Example Output</h2>
<pre>
Found 2 total goroutines

2 goroutines; example goroutine=182; state=[semacquire]
main.b2(0xc000192068)
	/Users/ej/combinestacks/stackdemo/stackdemo.go:86
main.b1(0xc000192068)
	/Users/ej/combinestacks/stackdemo/stackdemo.go:82
created by main.main
	/Users/ej/combinestacks/stackdemo/stackdemo.go:41
<pre>
</body>
</html>
`

func makeHandlers() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc(uploadPath, handleUpload)
	mux.HandleFunc(panicParsePath, handlePanicParse)
	return mux
}

func serveHTTP(addr string) error {
	mux := makeHandlers()
	log.Printf("listening on http://%s ...", addr)
	return http.ListenAndServe(addr, mux)
}

func main() {
	addr := flag.String("addr", "", "If set, address for HTTP requests. If not set, reads from stdin.")
	flag.Parse()

	if *addr == "" && os.Getenv(portEnvVar) != "" {
		*addr = ":" + os.Getenv(portEnvVar)
	}
	if *addr != "" {
		err := serveHTTP(*addr)
		if err != nil {
			panic(err)
		}
		return
	}

	routines, err := parse(os.Stdin)
	if err != nil {
		panic(err)
	}
	err = writeAggregated(os.Stdout, routines)
	if err != nil {
		panic(err)
	}

}
