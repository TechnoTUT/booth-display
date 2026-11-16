.PHONY: setup-ndi run-simulator ndi-stamp-sender all build build-frontend build-core build-backend submodules test run clean dev-frontend

# デフォルトターゲット: Vue.js・Rust・Go をまとめてビルド
all: build

# 1. Vue.js フロントエンド、2. Rust メディアコア (booth-core)、3. Go コントローラ
build: build-frontend build-core build-backend

# moip-engine サブモジュール (NDI バインディング) を取得
submodules:
	@test -f moip-engine/ndi/Cargo.toml || git submodule update --init --recursive

# Rust メディアコア: NDI 入力・ミキサー・エンコード・UDP 送出 (controller/bin/booth-core に配置)
build-core: submodules
	@echo "===> Building Rust media core (booth-core)..."
	cargo build --release --manifest-path core/Cargo.toml
	mkdir -p controller/bin
	install -m 755 core/target/release/booth-core controller/bin/booth-core

build-frontend:
	@echo "===> Building Vue 3 Frontend..."
	npm --prefix controller/frontend run build

build-backend:
	@echo "===> Building Go Controller..."
	cd controller && go build -o bin/display-controller ./cmd/server

# 単体テスト & E2E統合テストの実行
test: build-core
	@echo "===> Running Tests..."
	cargo test --release --manifest-path core/Cargo.toml
	cd controller && BOOTH_CORE_BIN=$(CURDIR)/controller/bin/booth-core go test -v ./internal/...

# コントローラの起動
run: build
	@echo "===> Starting Display Controller..."
	./controller/bin/display-controller -config config.yaml

# 開発用: Vite HMR 開発サーバの起動 (http://localhost:3000)
dev-frontend:
	npm --prefix controller/frontend run dev

# NDI ランタイム (libndi.so.6) を公式 NDI SDK からシステムに導入 (/usr/local/lib, sudo を使用)
setup-ndi:
	./scripts/install-ndi-sdk.sh

# 開発・検証用: Androidクライアント画面シミュレータの起動 (Web: http://localhost:9001)
# (依存パッケージはスクリプト先頭のインラインメタデータに記載、uv が自動で用意)
run-simulator:
	@echo "===> Starting Display 1 Simulator (UDP :8554, Web :9001)..."
	uv run tools/simulator/client_simulator.py --id "display-1" --port 8554 --web-port 9001

# 開発・検証用: NDI 遅延測定用のテスト送信ツール (booth-core の ndi_latency テスト用、システムの libndi を使用)
ndi-stamp-sender:
	cc -O2 -Wall -o tools/ndi-stamp-sender/ndi-stamp-sender tools/ndi-stamp-sender/ndi_stamp_sender.c -ldl

# ビルド成果物のクリーンアップ
clean:
	@echo "===> Cleaning build artifacts..."
	rm -rf controller/bin controller/web/dist tools/ndi-stamp-sender/ndi-stamp-sender
	cargo clean --manifest-path core/Cargo.toml
