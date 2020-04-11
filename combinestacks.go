package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

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
var fileLine = regexp.MustCompile(`([^ ]+):(\d+)($| \+| fp=).*$`)

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

func main() {
	routines, err := parse(os.Stdin)
	if err != nil {
		panic(err)
	}

	groups := map[stackHash][]routine{}
	for _, r := range routines {
		h := hash(r)
		groups[h] = append(groups[h], r)
	}

	fmt.Printf("parsed %d total goroutines\n", len(routines))
	for _, group := range groups {
		fmt.Printf("\n%d routines:\n", len(group))

		fmt.Printf("%s [%s]\n", group[0].label, group[0].state)
		for j, f := range group[0].stack {
			fmt.Printf("  %2d: %s(%s)\n", j, f.function, f.args)
			fmt.Printf("        %s:%d\n", f.file, f.line)
		}
		if group[0].created.function != "" {
			fmt.Printf("  created by %s\n", group[0].created.function)
			fmt.Printf("        %s:%d\n", group[0].created.file, group[0].created.line)
		}
	}
}
