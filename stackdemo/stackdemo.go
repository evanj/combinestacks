package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"
)

func main() {
	aStacks := flag.Int("numAStacks", 100, "Number of A stacks")
	bStacks := flag.Int("numBStacks", 50, "Number of B stacks")
	pprofAddr := flag.String("pprofAddr", "localhost:8080", "Address to listen for /debug/pprof; ''=disabled")
	oom := flag.Bool("oom", false, "If true will use memory until it gets killed/runs out, which should dump stacks")
	oomTouch := flag.Bool("oomTouch", true, "If true, will touch each page of memory that it allocates")
	writeStacks := flag.String("writeStacks", "", "Path to write stacks using pprof.Profile.WriteTo")
	writeStacksDebug := flag.Int("writeStacksDebug", 2, "pprof.Profile.WriteTo; 0=binary; 1=comments; 2=text")
	exit := flag.Bool("exit", false, "true: Exit immediately at end; false: block forever")
	panicAtEnd := flag.Bool("panic", false, "true: Panic at end of main()")
	oomChunkSizeMiB := flag.Int("oomChunkSizeMiB", 1, "Size of allocations when trying to run out of memory (MiB)")
	runningGoroutines := flag.Int("runningGoroutines", 0, "Goroutines that will be running; causes them to not be written in stacks")
	flag.Parse()

	if *pprofAddr != "" {
		log.Printf("listening on addr http://%s ...", *pprofAddr)
		go func() {
			err := http.ListenAndServe(*pprofAddr, nil)
			if err != nil {
				panic(err)
			}
		}()
	}

	// start the stacks
	blockAllStacks := sync.Mutex{}
	blockAllStacks.Lock()

	for i := 0; i < *aStacks; i++ {
		go a1(&blockAllStacks)
	}
	for i := 0; i < *bStacks; i++ {
		go b1(&blockAllStacks)
	}
	for i := 0; i < *runningGoroutines; i++ {
		go running()
	}

	if *writeStacks != "" {
		log.Printf("writing stacks to %s ...", *writeStacks)
		f, err := os.OpenFile(*writeStacks, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			panic(err)
		}
		err = pprof.Lookup("goroutine").WriteTo(f, *writeStacksDebug)
		if err != nil {
			panic(err)
		}
		err = f.Close()
		if err != nil {
			panic(err)
		}
	}

	if *oom {
		log.Printf("allocating memory of size=%d MiB; touch=%t ...", *oomChunkSizeMiB, *oomTouch)
		for {
			go useMemory(*oomChunkSizeMiB, *oomTouch, &blockAllStacks)
			time.Sleep(time.Millisecond)
		}
	}

	if *panicAtEnd {
		panic("panic at end of main()")
	}

	if *exit {
		return
	}

	log.Printf("blocking forever ...")
	blockAllStacks.Lock()
}

const oneMiB = 1024 * 1024

func useMemory(chunkSizeMiB int, touch bool, mu *sync.Mutex) {
	mem := make([]byte, chunkSizeMiB*oneMiB)
	if touch {
		// touch all pages to ensure they are allocated
		for i := 0; i < len(mem); i += 4096 {
			mem[i] = byte(i)
		}
	}
	mu.Lock()
	mu.Unlock()

	// ensure mem is held across the lock
	fmt.Println(mem[len(mem)-1])
}

func a1(mu *sync.Mutex) {
	a2(mu)
}

func a2(mu *sync.Mutex) {
	mu.Lock()
	mu.Unlock()
}

func b1(mu *sync.Mutex) {
	b2(mu)
}

func b2(mu *sync.Mutex) {
	mu.Lock()
	mu.Unlock()
}

func burnCPU() int {
	total := 0
	for i := 0; i < 10000000; i++ {
		total += i + total*total/(i+1)
	}
	return total
}

func running() {
	total := 0
	for {
		// do something to burn CPU so this stays "running"
		total = burnCPU()

		// with go1.13: necessary since otherwise this is a busy loop and GC can't run
		runtime.Gosched()
		if total == ^int(0) {
			break
		}
	}
	fmt.Println(total)
}
