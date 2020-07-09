# Combine Go Stacks

A tool to combine goroutines from the plain text stack trace output, making it easier to understand crashes from large program. [Try it in your browser: https://combinestacks.evanjones.ca/](https://combinestacks.evanjones.ca/). For details, see [my blog post](https://www.evanjones.ca/go-stack-traces.html). You probably should use [PanicParse instead](https://github.com/maruel/panicparse), it is much better.


## Usage

1. Get the stacks in the plain text format somehow.
2. Run `combinestacks < (file)`

You can also run the web version instead with `combinestacks --addr=localhost:8080`, or by setting the `PORT` environment variable.


## stackdemo

The stackdemo program provides a variety of command line flags to trigger different types of stack traces. I also put a number of different kinds I generated with this tool in the `testdata` directory.


## Forked Panic Parse

This enabled me to stick it in Google Cloud Run by adding the exportpanicparse package. I forked commit c9ea50b5eb3e9abc336bf2da9f2f7b7700454708.


## Wishlist

Things that I will probably never implement:

* Parse the stacks then output a profile, e.g. in pprof format. This would allow this to be connected to pprofweb: https://pprofweb.evanjones.ca/
* Create an HTML interface to allow you to expand/collapse stacks, and maybe display the different states for a collection of goroutines (e.g. running, blocked, etc?)


## Publishing

Requires access to Google:

* `docker build . --tag=us.gcr.io/gosignin-demo/combinestacks:$(date +%Y%m%d)`
* `docker push us.gcr.io/gosignin-demo/combinestacks:$(date +%Y%m%d)`
