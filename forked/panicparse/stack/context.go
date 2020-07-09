// Copyright 2018 Marc-Antoine Ruel. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

//go:generate go get golang.org/x/tools/cmd/stringer
//go:generate stringer -type state

package stack

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Context is a parsing context.
//
// It contains the deduced GOROOT and GOPATH, if guesspaths is true.
type Context struct {
	// Goroutines is the Goroutines found.
	//
	// They are in the order that they were printed.
	Goroutines []*Goroutine

	// GOROOT is the GOROOT as detected in the traceback, not the on the host.
	//
	// It can be empty if no root was determined, for example the traceback
	// contains only non-stdlib source references.
	//
	// Empty is guesspaths was false.
	GOROOT string
	// GOPATHs is the GOPATH as detected in the traceback, with the value being
	// the corresponding path mapped to the host.
	//
	// It can be empty if only stdlib code is in the traceback or if no local
	// sources were matched up. In the general case there is only one entry in
	// the map.
	//
	// Nil is guesspaths was false.
	GOPATHs map[string]string

	// localGomoduleRoot is the root directory containing go.mod. It is
	// considered to be the primary project containing the main executable. It is
	// initialized by findRoots().
	//
	// It only works with stack traces created in the local file system.
	localGomoduleRoot string
	// gomodImportPath is set to the relative import path that localGomoduleRoot
	// represents.
	gomodImportPath string

	// localgoroot is GOROOT with "/" as path separator. No trailing "/".
	localgoroot string
	// localgopaths is GOPATH with "/" as path separator. No trailing "/".
	localgopaths []string
}

// ParseDump processes the output from runtime.Stack().
//
// Returns nil *Context if no stack trace was detected.
//
// It pipes anything not detected as a panic stack trace from r into out. It
// assumes there is junk before the actual stack trace. The junk is streamed to
// out.
//
// If guesspaths is false, no guessing of GOROOT and GOPATH is done, and Call
// entites do not have LocalSrcPath and IsStdlib filled in. If true, be warned
// that file presence is done, which means some level of disk I/O.
func ParseDump(r io.Reader, out io.Writer, guesspaths bool) (*Context, error) {
	goroutines, err := parseDump(r, out)
	if len(goroutines) == 0 {
		return nil, err
	}
	c := &Context{
		Goroutines:   goroutines,
		localgoroot:  strings.Replace(runtime.GOROOT(), "\\", "/", -1),
		localgopaths: getGOPATHs(),
	}
	nameArguments(goroutines)
	// Corresponding local values on the host for Context.
	if guesspaths {
		c.findRoots()
		for _, r := range c.Goroutines {
			// Note that this is important to call it even if
			// c.GOROOT == c.localgoroot.
			r.updateLocations(c.GOROOT, c.localgoroot, c.localGomoduleRoot, c.gomodImportPath, c.GOPATHs)
		}
	}
	return c, err
}

// Private stuff.

func parseDump(r io.Reader, out io.Writer) ([]*Goroutine, error) {
	// Lines over 16k in length will not be accepted.
	br := bufio.NewReaderSize(r, 16*1024)
	s := scanningState{}
	wasLong := false
	for {
		slice, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull || wasLong {
			// Special case, the line is too long.
			wasLong = err == bufio.ErrBufferFull
			_, err = out.Write(slice)
		} else {
			p, err1 := s.scan(slice)
			if err1 != nil && (err == nil || err == io.EOF) {
				err = err1
			}
			if len(p) != 0 {
				if _, err1 = out.Write(p); err1 != nil && (err == nil || err == io.EOF) {
					err = err1
				}
			}
			if err == io.EOF {
				return s.goroutines, nil
			}
		}

		if err != nil {
			return s.goroutines, err
		}
	}

}

var (
	lockedToThread = []byte("locked to thread")
	framesElided   = []byte("...additional frames elided...")
	// gotRaceHeader1, normal
	raceHeaderFooter = []byte("==================")
	// gotRaceHeader2
	raceHeader = []byte("WARNING: DATA RACE")
	crlf       = []byte("\r\n")
	lf         = []byte("\n")
	commaSpace = []byte(", ")
	writeCap   = []byte("Write")
	writeLow   = []byte("write")
	threeDots  = []byte("...")
)

// These are effectively constants.
var (
	// gotRoutineHeader
	reRoutineHeader = regexp.MustCompile("^([ \t]*)goroutine (\\d+) \\[([^\\]]+)\\]\\:$")
	reMinutes       = regexp.MustCompile(`^(\d+) minutes$`)

	// gotUnavail
	reUnavail = regexp.MustCompile("^(?:\t| +)goroutine running on other thread; stack unavailable")

	// gotFileFunc, gotRaceOperationFile, gotRaceGoroutineFile
	// See gentraceback() in src/runtime/traceback.go for more information.
	// - Sometimes the source file comes up as "<autogenerated>". It is the
	//   compiler than generated these, not the runtime.
	// - The tab may be replaced with spaces when a user copy-paste it, handle
	//   this transparently.
	// - "runtime.gopanic" is explicitly replaced with "panic" by gentraceback().
	// - The +0x123 byte offset is printed when frame.pc > _func.entry. _func is
	//   generated by the linker.
	// - The +0x123 byte offset is not included with generated code, e.g. unnamed
	//   functions "func·006()" which is generally go func() { ... }()
	//   statements. Since the _func is generated at runtime, it's probably why
	//   _func.entry is not set.
	// - C calls may have fp=0x123 sp=0x123 appended. I think it normally happens
	//   when a signal is not correctly handled. It is printed with m.throwing>0.
	//   These are discarded.
	// - For cgo, the source file may be "??".
	reFile = regexp.MustCompile("^(?:\t| +)(\\?\\?|\\<autogenerated\\>|.+\\.(?:c|go|s))\\:(\\d+)(?:| \\+0x[0-9a-f]+)(?:| fp=0x[0-9a-f]+ sp=0x[0-9a-f]+(?:| pc=0x[0-9a-f]+))$")

	// gotCreated
	// Sadly, it doesn't note the goroutine number so we could cascade them per
	// parenthood.
	reCreated = regexp.MustCompile("^created by (.+)$")

	// gotFunc, gotRaceOperationFunc, gotRaceGoroutineFunc
	reFunc = regexp.MustCompile(`^(.+)\((.*)\)$`)

	// Race:
	// See https://github.com/llvm/llvm-project/blob/master/compiler-rt/lib/tsan/rtl/tsan_report.cpp
	// for the code generating these messages. Please note only the block in
	//   #else  // #if !SANITIZER_GO
	// is used.
	// TODO(maruel): "    [failed to restore the stack]\n\n"
	// TODO(maruel): "Global var %s of size %zu at %p declared at %s:%zu\n"

	// gotRaceOperationHeader
	reRaceOperationHeader = regexp.MustCompile(`^(Read|Write) at (0x[0-9a-f]+) by goroutine (\d+):$`)

	// gotRaceOperationHeader
	reRacePreviousOperationHeader = regexp.MustCompile(`^Previous (read|write) at (0x[0-9a-f]+) by goroutine (\d+):$`)

	// gotRaceGoroutineHeader
	reRaceGoroutine = regexp.MustCompile(`^Goroutine (\d+) \((running|finished)\) created at:$`)

	// TODO(maruel): Use it.
	//reRacePreviousOperationMainHeader = regexp.MustCompile("^Previous (read|write) at (0x[0-9a-f]+) by main goroutine:$")
)

// state is the state of the scan to detect and process a stack trace.
type state int

// Initial state is normal. Other states are when a stack trace is detected.
const (
	// Outside a stack trace.
	// to: gotRoutineHeader, raceHeader1
	normal state = iota

	// Panic stack trace:

	// Signature: ""
	// An empty line between goroutines.
	// from: gotFileCreated, gotFileFunc
	// to: gotRoutineHeader, normal
	betweenRoutine
	// Regexp: reRoutineHeader
	// Signature: "goroutine 1 [running]:"
	// Goroutine header was found.
	// from: normal
	// to: gotUnavail, gotFunc
	gotRoutineHeader
	// Regexp: reFunc
	// Signature: "main.main()"
	// Function call line was found.
	// from: gotRoutineHeader
	// to: gotFileFunc
	gotFunc
	// Regexp: reCreated
	// Signature: "created by main.glob..func4"
	// Goroutine creation line was found.
	// from: gotFileFunc
	// to: gotFileCreated
	gotCreated
	// Regexp: reFile
	// Signature: "\t/foo/bar/baz.go:116 +0x35"
	// File header was found.
	// from: gotFunc
	// to: gotFunc, gotCreated, betweenRoutine, normal
	gotFileFunc
	// Regexp: reFile
	// Signature: "\t/foo/bar/baz.go:116 +0x35"
	// File header was found.
	// from: gotCreated
	// to: betweenRoutine, normal
	gotFileCreated
	// Regexp: reUnavail
	// Signature: "goroutine running on other thread; stack unavailable"
	// State when the goroutine stack is instead is reUnavail.
	// from: gotRoutineHeader
	// to: betweenRoutine, gotCreated
	gotUnavail

	// Race detector:

	// Constant: raceHeaderFooter
	// Signature: "=================="
	// from: normal
	// to: normal, gotRaceHeader2
	gotRaceHeader1
	// Constant: raceHeader
	// Signature: "WARNING: DATA RACE"
	// from: gotRaceHeader1
	// to: normal, gotRaceOperationHeader
	gotRaceHeader2
	// Regexp: reRaceOperationHeader, reRacePreviousOperationHeader
	// Signature: "Read at 0x00c0000e4030 by goroutine 7:"
	// A race operation was found.
	// from: gotRaceHeader2
	// to: normal, gotRaceOperationFunc
	gotRaceOperationHeader
	// Regexp: reFunc
	// Signature: "  main.panicRace.func1()"
	// Function that caused the race.
	// from: gotRaceOperationHeader
	// to: normal, gotRaceOperationFile
	gotRaceOperationFunc
	// Regexp: reFile
	// Signature: "\t/foo/bar/baz.go:116 +0x35"
	// File header that caused the race.
	// from: gotRaceOperationFunc
	// to: normal, betweenRaceOperations, gotRaceOperationFunc
	gotRaceOperationFile
	// Signature: ""
	// Empty line between race operations or just after.
	// from: gotRaceOperationFile
	// to: normal, gotRaceOperationHeader, gotRaceGoroutineHeader
	betweenRaceOperations

	// Regexp: reRaceGoroutine
	// Signature: "Goroutine 7 (running) created at:"
	// Goroutine header.
	// from: betweenRaceOperations, betweenRaceGoroutines
	// to: normal, gotRaceOperationHeader
	gotRaceGoroutineHeader
	// Regexp: reFunc
	// Signature: "  main.panicRace.func1()"
	// Function that caused the race.
	// from: gotRaceGoroutineHeader
	// to: normal, gotRaceGoroutineFile
	gotRaceGoroutineFunc
	// Regexp: reFile
	// Signature: "\t/foo/bar/baz.go:116 +0x35"
	// File header that caused the race.
	// from: gotRaceGoroutineFunc
	// to: normal, betweenRaceGoroutines
	gotRaceGoroutineFile
	// Signature: ""
	// Empty line between race stack traces.
	// from: gotRaceGoroutineFile
	// to: normal, gotRaceGoroutineHeader
	betweenRaceGoroutines
)

// scanningState is the state of the scan to detect and process a stack trace
// and stores the traces found.
type scanningState struct {
	// goroutines contains all the goroutines found.
	goroutines []*Goroutine

	state          state
	prefix         []byte
	goroutineIndex int
}

// scan scans one line, updates goroutines and move to the next state.
//
// TODO(maruel): Handle corrupted stack cases:
// - missed stack barrier
// - found next stack barrier at 0x123; expected
// - runtime: unexpected return pc for FUNC_NAME called from 0x123
func (s *scanningState) scan(line []byte) ([]byte, error) {
	/* This is very useful to debug issues in the state machine.
	defer func() {
		log.Printf("scan(%q) -> %s", line, s.state)
	}()
	//*/
	var cur *Goroutine
	if len(s.goroutines) != 0 {
		cur = s.goroutines[len(s.goroutines)-1]
	}
	trimmed := line
	if bytes.HasSuffix(line, crlf) {
		trimmed = line[:len(line)-2]
	} else if bytes.HasSuffix(line, lf) {
		trimmed = line[:len(line)-1]
	} else {
		// There's two cases:
		// - It's the end of the stream and it's not terminating with EOL character.
		// - The line is longer than bufio.MaxScanTokenSize
		if s.state == normal {
			return line, nil
		}
		// Let it flow. It's possible the last line was trimmed and we still want to parse it.
	}

	if len(trimmed) != 0 && len(s.prefix) != 0 {
		// This can only be the case if s.state != normal or the line is empty.
		if !bytes.HasPrefix(trimmed, s.prefix) {
			prefix := s.prefix
			s.state = normal
			s.prefix = nil
			return nil, fmt.Errorf("inconsistent indentation: %q, expected %q", trimmed, prefix)
		}
		trimmed = trimmed[len(s.prefix):]
	}

	switch s.state {
	case normal:
		// We could look for '^panic:' but this is more risky, there can be a lot
		// of junk between this and the stack dump.
		fallthrough

	case betweenRoutine:
		// Look for a goroutine header.
		if match := reRoutineHeader.FindSubmatch(trimmed); match != nil {
			if id, ok := atou(match[2]); ok {
				// See runtime/traceback.go.
				// "<state>, \d+ minutes, locked to thread"
				items := bytes.Split(match[3], commaSpace)
				sleep := 0
				locked := false
				for i := 1; i < len(items); i++ {
					if bytes.Equal(items[i], lockedToThread) {
						locked = true
						continue
					}
					// Look for duration, if any.
					if match2 := reMinutes.FindSubmatch(items[i]); match2 != nil {
						sleep, _ = atou(match2[1])
					}
				}
				g := &Goroutine{
					Signature: Signature{
						State:    string(items[0]),
						SleepMin: sleep,
						SleepMax: sleep,
						Locked:   locked,
					},
					ID:    id,
					First: len(s.goroutines) == 0,
				}
				// Increase performance by always allocating 4 goroutines minimally.
				if s.goroutines == nil {
					s.goroutines = make([]*Goroutine, 0, 4)
				}
				s.goroutines = append(s.goroutines, g)
				s.state = gotRoutineHeader
				s.prefix = append([]byte{}, match[1]...)
				return nil, nil
			}
		}
		// Switch to race detection mode.
		if bytes.Equal(trimmed, raceHeaderFooter) {
			// TODO(maruel): We should buffer it in case the next line is not a
			// WARNING so we can output it back.
			s.state = gotRaceHeader1
			return nil, nil
		}
		// Fallthrough.
		s.state = normal
		s.prefix = nil
		return line, nil

	case gotRoutineHeader:
		if reUnavail.Match(trimmed) {
			// Generate a fake stack entry.
			cur.Stack.Calls = []Call{{SrcPath: "<unavailable>"}}
			// Next line is expected to be an empty line.
			s.state = gotUnavail
			return nil, nil
		}
		c := Call{}
		if found, err := parseFunc(&c, trimmed); found {
			// Increase performance by always allocating 4 calls minimally.
			if cur.Stack.Calls == nil {
				cur.Stack.Calls = make([]Call, 0, 4)
			}
			cur.Stack.Calls = append(cur.Stack.Calls, c)
			s.state = gotFunc
			return nil, err
		}
		return nil, fmt.Errorf("expected a function after a goroutine header, got: %q", bytes.TrimSpace(trimmed))

	case gotFunc:
		// cur.Stack.Calls is guaranteed to have at least one item.
		if found, err := parseFile(&cur.Stack.Calls[len(cur.Stack.Calls)-1], trimmed); err != nil {
			return nil, err
		} else if !found {
			return nil, fmt.Errorf("expected a file after a function, got: %q", bytes.TrimSpace(trimmed))
		}
		s.state = gotFileFunc
		return nil, nil

	case gotCreated:
		if found, err := parseFile(&cur.CreatedBy.Calls[0], trimmed); err != nil {
			return nil, err
		} else if !found {
			return nil, fmt.Errorf("expected a file after a created line, got: %q", trimmed)
		}
		s.state = gotFileCreated
		return nil, nil

	case gotFileFunc:
		if match := reCreated.FindSubmatch(trimmed); match != nil {
			cur.CreatedBy.Calls = make([]Call, 1)
			if err := cur.CreatedBy.Calls[0].Func.Init(string(match[1])); err != nil {
				cur.CreatedBy.Calls = nil
				return nil, err
			}
			s.state = gotCreated
			return nil, nil
		}
		if bytes.Equal(trimmed, framesElided) {
			cur.Stack.Elided = true
			// TODO(maruel): New state.
			return nil, nil
		}
		c := Call{}
		if found, err := parseFunc(&c, trimmed); found {
			// Increase performance by always allocating 4 calls minimally.
			if cur.Stack.Calls == nil {
				cur.Stack.Calls = make([]Call, 0, 4)
			}
			cur.Stack.Calls = append(cur.Stack.Calls, c)
			s.state = gotFunc
			return nil, err
		}
		if len(trimmed) == 0 {
			s.state = betweenRoutine
			return nil, nil
		}
		// Back to normal state.
		s.state = normal
		s.prefix = nil
		return line, nil

	case gotFileCreated:
		if len(trimmed) == 0 {
			s.state = betweenRoutine
			return nil, nil
		}
		s.state = normal
		s.prefix = nil
		return line, nil

	case gotUnavail:
		if len(trimmed) == 0 {
			s.state = betweenRoutine
			return nil, nil
		}
		if match := reCreated.FindSubmatch(trimmed); match != nil {
			cur.CreatedBy.Calls = make([]Call, 1)
			if err := cur.CreatedBy.Calls[0].Func.Init(string(match[1])); err != nil {
				cur.CreatedBy.Calls = nil
				return nil, err
			}
			s.state = gotCreated
			return nil, nil
		}
		return nil, fmt.Errorf("expected empty line after unavailable stack, got: %q", bytes.TrimSpace(trimmed))

		// Race detector.

	case gotRaceHeader1:
		if bytes.Equal(trimmed, raceHeader) {
			// TODO(maruel): We should buffer it in case the next line is not a
			// WARNING so we can output it back.
			s.state = gotRaceHeader2
			return nil, nil
		}
		s.state = normal
		return line, nil

	case gotRaceHeader2:
		if match := reRaceOperationHeader.FindSubmatch(trimmed); match != nil {
			w := bytes.Equal(match[1], writeCap)
			addr, err := strconv.ParseUint(string(match[2]), 0, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse address on line: %q", bytes.TrimSpace(trimmed))
			}
			id, ok := atou(match[3])
			if !ok {
				return nil, fmt.Errorf("failed to parse goroutine id on line: %q", bytes.TrimSpace(trimmed))
			}
			if s.goroutines != nil {
				panic("internal failure; expected s.goroutines to be nil")
			}
			s.goroutines = append(make([]*Goroutine, 0, 4), &Goroutine{ID: id, First: true, RaceWrite: w, RaceAddr: addr})
			s.goroutineIndex = len(s.goroutines) - 1
			s.state = gotRaceOperationHeader
			return nil, nil
		}
		s.state = normal
		return line, nil

	case gotRaceOperationHeader:
		c := Call{}
		if found, err := parseFunc(&c, trimLeftSpace(trimmed)); found {
			// Increase performance by always allocating 4 calls minimally.
			if cur.Stack.Calls == nil {
				cur.Stack.Calls = make([]Call, 0, 4)
			}
			cur.Stack.Calls = append(cur.Stack.Calls, c)
			s.state = gotRaceOperationFunc
			return nil, err
		}
		return nil, fmt.Errorf("expected a function after a race operation, got: %q", trimmed)

	case gotRaceOperationFunc:
		if found, err := parseFile(&cur.Stack.Calls[len(cur.Stack.Calls)-1], trimmed); err != nil {
			return nil, err
		} else if !found {
			return nil, fmt.Errorf("expected a file after a race function, got: %q", trimmed)
		}
		s.state = gotRaceOperationFile
		return nil, nil

	case gotRaceOperationFile:
		if len(trimmed) == 0 {
			s.state = betweenRaceOperations
			return nil, nil
		}
		c := Call{}
		if found, err := parseFunc(&c, trimLeftSpace(trimmed)); found {
			cur.Stack.Calls = append(cur.Stack.Calls, c)
			s.state = gotRaceOperationFunc
			return nil, err
		}
		return nil, fmt.Errorf("expected an empty line after a race file, got: %q", trimmed)

	case betweenRaceOperations:
		// Look for other previous race data operations.
		if match := reRacePreviousOperationHeader.FindSubmatch(trimmed); match != nil {
			w := bytes.Equal(match[1], writeLow)
			addr, err := strconv.ParseUint(string(match[2]), 0, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse address on line: %q", bytes.TrimSpace(trimmed))
			}
			id, ok := atou(match[3])
			if !ok {
				return nil, fmt.Errorf("failed to parse goroutine id on line: %q", bytes.TrimSpace(trimmed))
			}
			s.goroutines = append(s.goroutines, &Goroutine{ID: id, RaceWrite: w, RaceAddr: addr})
			s.goroutineIndex = len(s.goroutines) - 1
			s.state = gotRaceOperationHeader
			return nil, nil
		}
		fallthrough

	case betweenRaceGoroutines:
		if match := reRaceGoroutine.FindSubmatch(trimmed); match != nil {
			id, ok := atou(match[1])
			if !ok {
				return nil, fmt.Errorf("failed to parse goroutine id on line: %q", bytes.TrimSpace(trimmed))
			}
			found := false
			for i, g := range s.goroutines {
				if g.ID == id {
					g.State = string(match[2])
					s.goroutineIndex = i
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("unexpected goroutine ID on line: %q", bytes.TrimSpace(trimmed))
			}
			s.state = gotRaceGoroutineHeader
			return nil, nil
		}
		return nil, fmt.Errorf("expected an operator or goroutine, got: %q", trimmed)

		// Race stack traces

	case gotRaceGoroutineFunc:
		c := s.goroutines[s.goroutineIndex].CreatedBy.Calls
		if found, err := parseFile(&c[len(c)-1], trimmed); err != nil {
			return nil, err
		} else if !found {
			return nil, fmt.Errorf("expected a file after a race function, got: %q", trimmed)
		}
		// TODO(maruel): Set s.goroutines[].CreatedBy.
		s.state = gotRaceGoroutineFile
		return nil, nil

	case gotRaceGoroutineFile:
		if len(trimmed) == 0 {
			s.state = betweenRaceGoroutines
			return nil, nil
		}
		if bytes.Equal(trimmed, raceHeaderFooter) {
			// Done.
			s.state = normal
			return nil, nil
		}
		fallthrough

	case gotRaceGoroutineHeader:
		c := Call{}
		if found, err := parseFunc(&c, trimLeftSpace(trimmed)); found {
			s.goroutines[s.goroutineIndex].CreatedBy.Calls = append(s.goroutines[s.goroutineIndex].CreatedBy.Calls, c)
			s.state = gotRaceGoroutineFunc
			return nil, err
		}
		return nil, fmt.Errorf("expected a function after a race operation or a race file, got: %q", trimmed)

	default:
		return nil, errors.New("internal error")
	}
}

// parseFunc only return an error if also returning a Call.
//
// Uses reFunc.
func parseFunc(c *Call, line []byte) (bool, error) {
	if match := reFunc.FindSubmatch(line); match != nil {
		if err := c.Func.Init(string(match[1])); err != nil {
			return true, err
		}
		for _, a := range bytes.Split(match[2], commaSpace) {
			if bytes.Equal(a, threeDots) {
				c.Args.Elided = true
				continue
			}
			if len(a) == 0 {
				// Remaining values were dropped.
				break
			}
			v, err := strconv.ParseUint(string(a), 0, 64)
			if err != nil {
				return true, fmt.Errorf("failed to parse int on line: %q", bytes.TrimSpace(line))
			}
			// Increase performance by always allocating 4 values minimally.
			if c.Args.Values == nil {
				c.Args.Values = make([]Arg, 0, 4)
			}
			// Assume the stack was generated with the same bitness (32 vs 64) than
			// the code processing it.
			c.Args.Values = append(c.Args.Values, Arg{Value: v, IsPtr: v > pointerFloor && v < pointerCeiling})
		}
		return true, nil
	}
	return false, nil
}

// parseFile only return an error if also processing a Call.
//
// Uses reFile.
func parseFile(c *Call, line []byte) (bool, error) {
	if match := reFile.FindSubmatch(line); match != nil {
		num, ok := atou(match[2])
		if !ok {
			return true, fmt.Errorf("failed to parse int on line: %q", bytes.TrimSpace(line))
		}
		c.init(string(match[1]), num)
		return true, nil
	}
	return false, nil
}

// hasSrcPrefix returns true if any of s is the prefix of p.
func hasSrcPrefix(p string, s map[string]string) bool {
	for prefix := range s {
		if strings.HasPrefix(p, prefix+"/src/") || strings.HasPrefix(p, prefix+"/pkg/mod/") {
			return true
		}
	}
	return false
}

// getFiles returns all the source files deduped and ordered.
func getFiles(goroutines []*Goroutine) []string {
	files := map[string]struct{}{}
	for _, g := range goroutines {
		for _, c := range g.Stack.Calls {
			files[c.SrcPath] = struct{}{}
		}
	}
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, len(files))
	for f := range files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// splitPath splits a path using "/" as separator into its components.
//
// The first item has its initial path separator kept.
func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	var out []string
	s := ""
	for _, c := range p {
		if c != '/' || (len(out) == 0 && strings.Count(s, "/") == len(s)) {
			s += string(c)
		} else if s != "" {
			out = append(out, s)
			s = ""
		}
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// isFile returns true if the path is a valid file.
func isFile(p string) bool {
	// TODO(maruel): Is it faster to open the file or to stat it? Worth a perf
	// test on Windows.
	i, err := os.Stat(p)
	return err == nil && !i.IsDir()
}

// rootedIn returns a root if the file split in parts is rooted in root.
//
// Uses "/" as path separator.
func rootedIn(root string, parts []string) string {
	//log.Printf("rootIn(%s, %v)", root, parts)
	for i := 1; i < len(parts); i++ {
		suffix := pathJoin(parts[i:]...)
		if isFile(pathJoin(root, suffix)) {
			return pathJoin(parts[:i]...)
		}
	}
	return ""
}

// reModule find the module line in a go.mod file. It works even on CRLF file.
var reModule = regexp.MustCompile(`(?m)^module\s+([^\n\r]+)\r?$`)

// isGoModule returns the string to the directory containing a go.mod/go.sum
// files pair, and the go import path it represents, if found.
func isGoModule(parts []string) (string, string) {
	for i := len(parts); i > 0; i-- {
		prefix := pathJoin(parts[:i]...)
		if isFile(pathJoin(prefix, "go.sum")) {
			b, err := ioutil.ReadFile(pathJoin(prefix, "go.mod"))
			if err != nil {
				continue
			}
			if match := reModule.FindSubmatch(b); match != nil {
				return prefix, string(match[1])
			}
		}
	}
	return "", ""
}

// findRoots sets member GOROOT, GOPATHs and localGomoduleRoot.
//
// This causes disk I/O as it checks for file presence.
//
// Returns the number of missing files.
func (c *Context) findRoots() int {
	c.GOPATHs = map[string]string{}
	missing := 0
	for _, f := range getFiles(c.Goroutines) {
		// TODO(maruel): Could a stack dump have mixed cases? I think it's
		// possible, need to confirm and handle.
		//log.Printf("  Analyzing %s", f)

		// First checks skip file I/O.
		if c.GOROOT != "" && strings.HasPrefix(f, c.GOROOT+"/src/") {
			// stdlib.
			continue
		}
		if hasSrcPrefix(f, c.GOPATHs) {
			// $GOPATH/src or go.mod dependency in $GOPATH/pkg/mod.
			continue
		}

		// At this point, disk will be looked up.
		parts := splitPath(f)
		if c.GOROOT == "" {
			if r := rootedIn(c.localgoroot+"/src", parts); r != "" {
				c.GOROOT = r[:len(r)-4]
				//log.Printf("Found GOROOT=%s", c.GOROOT)
				continue
			}
		}
		found := false
		for _, l := range c.localgopaths {
			if r := rootedIn(l+"/src", parts); r != "" {
				//log.Printf("Found GOPATH=%s", r[:len(r)-4])
				c.GOPATHs[r[:len(r)-4]] = l
				found = true
				break
			}
			if r := rootedIn(l+"/pkg/mod", parts); r != "" {
				//log.Printf("Found GOPATH=%s", r[:len(r)-8])
				c.GOPATHs[r[:len(r)-8]] = l
				found = true
				break
			}
		}
		// If the source is not found, it's probably a go module.
		if !found {
			if c.localGomoduleRoot == "" && len(parts) > 1 {
				// Search upward looking for a go.mod/go.sum pair.
				c.localGomoduleRoot, c.gomodImportPath = isGoModule(parts[:len(parts)-1])
			}
			if c.localGomoduleRoot != "" && strings.HasPrefix(f, c.localGomoduleRoot+"/") {
				continue
			}
		}
		if !found {
			// If the source is not found, just too bad.
			//log.Printf("Failed to find locally: %s", f)
			missing++
		}
	}
	return missing
}

// getGOPATHs returns parsed GOPATH or its default, using "/" as path separator.
func getGOPATHs() []string {
	var out []string
	if gp := os.Getenv("GOPATH"); gp != "" {
		for _, v := range filepath.SplitList(gp) {
			// Disallow non-absolute paths?
			if v != "" {
				v = strings.Replace(v, "\\", "/", -1)
				// Trim trailing "/".
				if l := len(v); v[l-1] == '/' {
					v = v[:l-1]
				}
				out = append(out, v)
			}
		}
	}
	if len(out) == 0 {
		homeDir := ""
		u, err := user.Current()
		if err != nil {
			homeDir = os.Getenv("HOME")
			if homeDir == "" {
				panic(fmt.Sprintf("Could not get current user or $HOME: %s\n", err.Error()))
			}
		} else {
			homeDir = u.HomeDir
		}
		out = []string{strings.Replace(homeDir+"/go", "\\", "/", -1)}
	}
	return out
}

// atou is a fast Atoi() function.
//
// It is a very simplified version of strconv.Atoi() that it never go into the
// slow path and it operates on []byte instead of string so it doesn't do
// memory allocation. It will fail on edge cases like prefix of zeros and other
// things that the panic stack trace generator never outputs.
//
// It doesn't handle negative values.
func atou(s []byte) (int, bool) {
	if l := len(s); strconv.IntSize == 32 && (0 < l && l < 10) || strconv.IntSize == 64 && (0 < l && l < 19) {
		n := 0
		for _, ch := range s {
			if ch -= '0'; ch > 9 {
				return 0, false
			}
			n = n*10 + int(ch)
		}
		return n, true
	}
	return 0, false
}

// trimLeftSpace is the faster equivalent of bytes.TrimLeft(s, "\t ").
func trimLeftSpace(s []byte) []byte {
	for i, ch := range s {
		if ch != '\t' && ch != ' ' {
			return s[i:]
		}
	}
	return nil
}