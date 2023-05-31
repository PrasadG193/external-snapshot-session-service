GOOS ?= linux
GOARCH ?= amd64

IMAGE_REPO_SERVER ?= prasadg193/external-snapshot-session-service
IMAGE_TAG_SERVER ?= latest
IMAGE_REPO_CLIENT ?= prasadg193/sample-cbt-client
IMAGE_TAG_CLIENT ?= latest

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

image: build
	docker build --platform=linux/amd64 -t $(IMAGE_REPO_SERVER):$(IMAGE_TAG_SERVER) -f Dockerfile-grpc .
	docker build --platform=linux/amd64 -t $(IMAGE_REPO_CLIENT):$(IMAGE_TAG_CLIENT) -f Dockerfile-grpc-client .

push:
	docker push $(IMAGE_REPO_SERVER):$(IMAGE_TAG_SERVER)
	docker push $(IMAGE_REPO_CLIENT):$(IMAGE_TAG_CLIENT)
