# Build the Linux binary that ops/ansible ships to the servers.
bin/app: main.go
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $@ .
