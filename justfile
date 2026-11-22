set shell := ["bash", "-c"]

build:
  go build -o ./target/mmapdb_example ./cmd/mmapdb_example

run: build
  ./target/mmapdb_example

test_build:
  go build -o ./target/allocator_test ./tests/allocator_test/

test: test_build
  ./target/allocator_test
