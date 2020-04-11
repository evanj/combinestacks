# Combine Go Stacks

A tool to combine Goroutine stacks that are the same from unrecovered panic output, making it easier to understand crashes. This helps summarize what a big program with many goroutines was doing when it crashed.

Try it: https://combinestacks.evanjones.ca/

More details: https://www.evanjones.ca/hack-combine-stacks.html


If you record pprof data via /debug/pprof/goroutine or `pprof.Profile.WriteTo`, you can use `go tool pprof` to visualize it. I recommend using the web interface with `-http=localhost:8080` or https://pprofweb.evanjones.ca/ for my hacky publicly accessible version.


## Dumping stacks

Go provides a few different ways to dump all the stacks in a process:

* `/debug/pprof/goroutine`, `pprof.Profile.WriteTo(..., debug=0)`: The binary pprof format. You will need to use `go tool pprof` to view this.
* `/debug/pprof/goroutine?debug=1`, `pprof.Profile.WriteTo(..., debug=1)`: Plain text with comments with details.
* `/debug/pprof/goroutine?debug=2`, `pprof.Profile.WriteTo(..., debug=2)`: Plain text in the same format as unrecovered panic.
* GOTRACEBACK=all unrecovered panic: This happens if the processes runs out of memory or has another uncaught panic see: https://golang.org/pkg/runtime/#hdr-Environment_Variables


## OOM 

uname -r: Debian 10 on Google Cloud: 4.19.0-8-cloud-amd64

/proc/sys/vm/overcommit_memory = 1: Basically the OOM killer is always invoked because every allocation "works" and it defers until the page is accessed
/proc/sys/vm/overcommit_memory = 0: Go fails with runtime out of memory

