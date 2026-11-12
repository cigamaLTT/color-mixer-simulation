.PHONY: help build build-host build-simulation build-esp32 run demo test test-host test-integration test-core test-simulation verify-wokwi verify-idf-wokwi wokwi clean

help:
	@printf '%s\n' \
	  'make build             Build ESP32 firmware, backend, native device, and Wokwi custom chip' \
	  'make run               Run backend/dashboard only (waits for a real device)' \
	  'make demo              Run backend plus native Rust controller and software plant' \
	  'make test              Unit, model, and real network integration tests' \
	  'make verify-wokwi      Exercise an already-running actual Wokwi ESP32' \
	  'make verify-idf-wokwi  Run one ESP-IDF Wokwi acceptance batch' \
	  'make build-esp32       Cross-compile the actual ESP32 firmware' \
	  'make wokwi             Run Wokwi CLI (requires WOKWI_CLI_TOKEN)' \
	  'make clean             Remove generated project build artifacts'

build: build-host build-simulation build-esp32

build-host:
	./scripts/build-host.sh

build-simulation:
	./simulation/build.sh

build-esp32:
	./firmware/build-esp32.sh

run:
	./scripts/run.sh

demo:
	./scripts/demo.sh

test: test-core test-simulation test-integration

test-host: test

test-core:
	cargo test --locked --manifest-path firmware/Cargo.toml -p mixer-core
	cd iot && go test -race ./...

test-simulation:
	./simulation/test.sh

test-integration: build-host
	python3 -m unittest discover -s tests -p 'test_*.py' -v

verify-wokwi:
	@python3 scripts/verify-wokwi.py $(VERIFY_WOKWI_ARGS)

verify-idf-wokwi: build-host
	@python3 scripts/run-idf-wokwi-cycle.py

wokwi: build-simulation build-esp32
	./scripts/wokwi.sh

clean:
	rm -rf .build firmware/target simulation/build simulation/.build simulation/test-output tests/__pycache__
