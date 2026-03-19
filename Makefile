# Makefile - 修复 GOROOT 指向错误路径时 go 命令无法运行的问题
# 当 GOROOT 指向 /usr/local/Cellar/go/... 等不存在的路径时，改用 /usr/local/go
ifneq ($(wildcard $(GOROOT)/src/runtime),)
  # GOROOT 有效，无需覆盖
else
  ifeq ($(wildcard /usr/local/go/src/runtime),)
    # 尝试 homebrew 默认路径
    export GOROOT := $(shell brew --prefix go 2>/dev/null)/libexec
  else
    export GOROOT := /usr/local/go
  endif
endif

.PHONY: build test run
build:
	go build ./...

test:
	go test ./...

run:
	go run .
