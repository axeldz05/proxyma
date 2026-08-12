BINARY_NAME=proxyma
GO=go
GOMOBILE=$(GO) tool gomobile
GRADLE=gradle
JAVA=$(if $(JAVA_HOME),$(JAVA_HOME)/bin/java,java)
ANDROID_GRADLE_COMPAT_JAVA=21
LINTER=golangci-lint
TEMP_DIR=/tmp/proxyma-dev
ANDROID_DIR=cmd/proxyma-android

# Colores para la terminal
BLUE=\033[0;34m
NC=\033[0m # No Color

.PHONY: all build test test-integration test-race test-android-bind test-android lint clean help init-cluster test-e2e test-e2e-pr test-e2e-network test-e2e-full test-all coverage

all: lint test build ## Run lint, tests, and build

build: ## Build the Proxyma CLI
	@echo "$(BLUE)Compiling Proxyma...$(NC)"
	$(GO) build -o "$(BINARY_NAME)" ./cmd/proxyma

test: ## Run the Go test suite
	@echo "$(BLUE)Running tests...$(NC)"
	$(GO) test -v ./...

test-integration: ## Run live public-contract integration tests
	@echo "$(BLUE)Running integration contract tests...$(NC)"
	$(GO) test -v ./cmd/proxyma-bind ./internal/server

# The race suite is green. It remains separate from `test` because it is slower.
test-race: ## Run the Go test suite with race detection
	@echo "$(BLUE)Running tests with the race detector...$(NC)"
	$(GO) test -race ./...

test-android-bind: ## Build the Android AAR and verify required Java APIs
test-android: ## Build and test Android against one fresh temporary AAR
test-android-bind test-android:
	@echo "$(BLUE)Building Android bindings for $@...$(NC)"
	@set -eu; \
	tmp_root="$${TMPDIR:-/tmp}"; \
	work_dir="$$(mktemp -d "$$tmp_root/proxyma-android-bind.XXXXXX")"; \
	trap 'rm -rf "$$work_dir"' EXIT HUP INT TERM; \
	aar="$$work_dir/proxyma.aar"; \
	classes_jar="$$work_dir/classes.jar"; \
	javap_output="$$work_dir/Proxyma_bind.javap"; \
	$(GOMOBILE) bind -o "$$aar" -target=android -androidapi=21 ./cmd/proxyma-bind; \
	unzip -p "$$aar" classes.jar > "$$classes_jar"; \
	javap -classpath "$$classes_jar" -public -s proxyma_bind.Proxyma_bind > "$$javap_output"; \
	awk '$$0 == "  public static native java.lang.String runService(java.lang.String, java.lang.String);" { run_decl=NR; next } \
		NR == run_decl+1 && $$0 == "    descriptor: (Ljava/lang/String;Ljava/lang/String;)Ljava/lang/String;" { run_service=1 } \
		$$0 == "  public static native java.lang.String resolveTaskResultPath(java.lang.String);" { resolve_decl=NR; next } \
		NR == resolve_decl+1 && $$0 == "    descriptor: (Ljava/lang/String;)Ljava/lang/String;" { resolve_path=1 } \
		$$0 == "  public static native java.lang.String cancelStream(java.lang.String);" { cancel_decl=NR; next } \
		NR == cancel_decl+1 && $$0 == "    descriptor: (Ljava/lang/String;)Ljava/lang/String;" { cancel_stream=1 } \
		END { if (!run_service || !resolve_path || !cancel_stream) { print "missing or incompatible required Java binding API"; exit 1 } }' "$$javap_output"; \
	if [ "$@" = "test-android" ]; then \
		java_version="$$($(JAVA) -XshowSettings:properties -version 2>&1 | { \
			while IFS= read -r line; do \
				case "$$line" in \
					*java.specification.version*) printf "%s\n" "$${line##*= }"; break ;; \
				esac; \
			done; \
		})"; \
		case "$$java_version" in \
			1.*) java_major="$${java_version#1.}"; java_major="$${java_major%%.*}" ;; \
			*) java_major="$${java_version%%.*}" ;; \
		esac; \
		if [ -z "$$java_major" ]; then echo "Unable to detect Gradle Java major version"; exit 1; fi; \
		gradle_java_option=""; \
		if [ "$$java_major" -gt "$(ANDROID_GRADLE_COMPAT_JAVA)" ]; then \
			gradle_java_option="-Djava.version=$(ANDROID_GRADLE_COMPAT_JAVA)"; \
			echo "Android Gradle compatibility: Java $$java_major detected; using java.version=$(ANDROID_GRADLE_COMPAT_JAVA)."; \
		fi; \
		gradle_user_home="$$work_dir/home"; \
		android_user_home="$$gradle_user_home/.android"; \
		gradle_cache_home="$${GRADLE_USER_HOME:-$${HOME:-$$gradle_user_home}/.gradle}"; \
		mkdir -p "$$android_user_home"; \
		GRADLE_USER_HOME="$$gradle_cache_home" ANDROID_USER_HOME="$$android_user_home" \
			$(GRADLE) $$gradle_java_option "-Duser.home=$$gradle_user_home" --no-daemon \
			-p "$(ANDROID_DIR)" -PproxymaAar="$$aar" testDebugUnitTest lintDebug assembleDebug; \
	fi

lint: ## Run golangci-lint
	@echo "$(BLUE)Running golangci-lint...$(NC)"
	$(LINTER) run

clean: ## Remove build and development artifacts
	@echo "$(BLUE)Cleaning...$(NC)"
	rm -f "$(BINARY_NAME)"
	rm -rf "$(TEMP_DIR)"

init-cluster: build ## Initialize a local cluster in TEMP_DIR
	@echo "$(BLUE)Initializing local cluster...$(NC)"
	mkdir -p "$(TEMP_DIR)"
	"$(abspath $(BINARY_NAME))" init --storage "$(TEMP_DIR)"

help: ## Show available targets
	@echo "Available targets:"
	@awk 'BEGIN { FS = ":.*## " } /^[[:alnum:]_.-]+:.*## / { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

test-e2e: test-e2e-full ## Run the stable full E2E profile

test-e2e-pr: ## Run deterministic PR E2E contracts
	@echo "$(BLUE)Running deterministic PR E2E contracts...$(NC)"
	E2E_PROFILE=pr ./tests/e2e/run.sh

test-e2e-network: ## Run network and fault-injection E2E tests
	@echo "$(BLUE)Running network E2E tests...$(NC)"
	E2E_PROFILE=network ./tests/e2e/run.sh

test-e2e-full: ## Run all stable E2E tests
	@echo "$(BLUE)Running the stable full E2E suite...$(NC)"
	E2E_PROFILE=full ./tests/e2e/run.sh

test-all: test test-e2e ## Run unit and E2E tests

coverage: ## Generate the unified coverage report
	@echo "$(BLUE)Generating unified coverage report...$(NC)"
	chmod +x ./scripts/coverage.sh
	./scripts/coverage.sh
