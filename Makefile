GOOS ?= linux
GOARCH ?= amd64

IMAGE_REPO_SERVER ?= prasadg193/ext-snap-session-svc
IMAGE_TAG_SERVER ?= latest

.PHONY: proto
proto:
	protoc -I=proto \
		--go_out=pkg/grpc --go_opt=paths=source_relative \
   	--go-grpc_out=pkg/grpc --go-grpc_opt=paths=source_relative \
		proto/*.proto


.PHONY: build
build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build  -o grpc-server ./cmd/server/main.go
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build  -o grpc-client ./cmd/client/main.go

image:
	docker build -t $(IMAGE_REPO_SERVER):$(IMAGE_TAG_SERVER) -f Dockerfile-grpc .

push:
	docker push $(IMAGE_REPO_SERVER):$(IMAGE_TAG_SERVER)


