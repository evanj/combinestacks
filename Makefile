docker:
	docker build . --tag=us.gcr.io/gosignin-demo/combinestacks:$(shell date '+%Y%m%d')-$(shell git rev-parse --short=10 HEAD)
