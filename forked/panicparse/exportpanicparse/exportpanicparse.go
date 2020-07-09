package exportpanicparse

import (
	"io"
	"log"

	"github.com/evanj/combinestacks/forked/panicparse/internal/htmlstack"
	"github.com/evanj/combinestacks/forked/panicparse/stack"
)

// ProcessHTML parses stacks from in and writes HTML to out.
func ProcessHTML(in io.Reader, out io.Writer) error {
	// Mostly stolen from panicparse/internal.process
	const rebase = false
	c, err := stack.ParseDump(in, out, rebase)
	if c == nil || err != nil {
		return err
	}
	if rebase {
		log.Printf("GOROOT=%s", c.GOROOT)
		log.Printf("GOPATH=%s", c.GOPATHs)
	}
	const needsEnv = false

	s := stack.AnyPointer
	buckets := stack.Aggregate(c.Goroutines, s)
	return htmlstack.WriteBuckets(out, buckets, needsEnv, false)
}
