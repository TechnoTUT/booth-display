# booth-display

LG Display製 LD290EJS-FPN1 (1920x540) を使用した連接ブースディスプレイを制御・同期表示するためのコントローラシステムです。

Web-GUIを内包したスタンドアロン単一バイナリとして動作し、全体仮想キャンバスの映像をディスプレイごとにクロップ分割して低遅延UDP (H.264 Annex-B) で同時送出します。

詳細なアーキテクチャやプロトコル仕様は [docs/](docs/) を参照してください。
- システムアーキテクチャ: [docs/architecture.md](docs/architecture.md)
- 映像伝送プロトコル仕様: [docs/streaming-protocol.md](docs/streaming-protocol.md)
- 開発時の注意事項・環境設定: [docs/development.md](docs/development.md)

## 必要要件

- OS: Linux (Debian 12+, Ubuntu 22.04+, AlmaLinux / RHEL 9+, Fedora 39+ など)
- Go: 1.26 以上
- Node.js: 20 以上
- FFmpeg: 6.0 以上 (`libx264` 有効、Intel GPU 利用時は `h264_vaapi` 有効)
- Intel VA-API ドライバ (推奨: 第6世代 Skylake 以降の Intel CPU 内蔵グラフィックスで低CPU負荷・高速エンコードを実現)
- uv: Python パッケージマネージャ (NDI 入力利用時、およびクライアントシミュレータ利用時)
- (Android クライアントをビルドする場合)
  - JDK 17 以上
  - Android SDK (`platforms;android-34`, `build-tools;34.0.0`)
  - SDK の場所を `android-client/local.properties` の `sdk.dir=...` または環境変数 `ANDROID_HOME` で指定

---

## FFmpeg & ハードウェア支援 (VA-API) セットアップ

コントローラは、Intel CPU 内蔵グラフィックス (Intel HD / UHD Graphics) の **VA-API (`h264_vaapi`)** によるハードウェア支援エンコードに対応しています。CPU 負荷を最小限に抑え、複数ディスプレイへの 30fps 同時リアルタイム送出を安定化できます（VA-API 非搭載環境では自動的に CPU ソフトウェアエンコード `libx264` にフォールバックします）。

### 1. パッケージのインストール

#### AlmaLinux / Rocky Linux / RHEL / Fedora
```bash
# RPM Fusion / EPEL リポジトリの有効化 (未導入の場合)
sudo dnf install -y epel-release
sudo dnf install -y --nogpgcheck https://mirrors.rpmfusion.org/free/el/rpmfusion-free-release-$(rpm -E %rhel).noarch.rpm

# FFmpeg および VA-API ドライバ・診断ツールのインストール
sudo dnf install -y ffmpeg libva-utils intel-media-driver
```

#### Ubuntu / Debian
```bash
sudo apt update
sudo apt install -y ffmpeg vainfo intel-media-va-driver-non-free
# (オープンソース版ドライバを利用する場合は intel-media-driver または intel-media-va-driver)
```

### 2. 動作確認

```bash
# VA-API デバイスが認識されているか確認 (renderD128 等が存在すること)
ls -l /dev/dri/renderD*

# H.264 エンコード (VAEntrypointEncSlice) に対応しているか確認
vainfo

# FFmpeg が VA-API エンコーダに対応しているか確認
ffmpeg -hide_banner -encoders | grep h264_vaapi
```

> **Note**: 実行ユーザが `/dev/dri/renderD128` にアクセス権を持つ必要があります（必要に応じて `sudo usermod -aG render $USER` または `video` グループに追加して再ログインしてください）。

---

## ビルド手順

ルートディレクトリの Makefile を使って、フロントエンド (Vue 3) とバックエンド (Go) を一括ビルドできます。フロントエンドのビルド成果物は Go バイナリ内に静的に埋め込まれます。

```bash
# フロントエンド・バックエンドの一括ビルド
make build

# テストの実行
make test
```

ビルドが完了すると、`controller/bin/display-controller` が生成されます。

開発時用の個別コマンド:
```bash
make build-frontend  # フロントエンドのみビルド
make build-backend   # Go コントローラのみビルド
make dev-frontend    # フロントエンド開発サーバ起動 (Vite)
```

---

## 起動方法

生成されたバイナリに設定ファイルを指定して実行します。

```bash
# 通常起動
./controller/bin/display-controller -config config.yaml

# または make コマンドで起動
make run
```

起動後、ブラウザで以下のURLを開きます。
- Web-GUI ダッシュボード: `http://localhost:8080`

---

## Androidクライアントアプリ (android-client/)

LG Display LD290EJS-FPN1 (Android 7.1 / 1920x540) 上で動作する映像受信・表示アプリです。

### 特徴
- MediaCodec によるハードウェア H.264 デコード (SurfaceView 直接描画)
- 1GB RAM 向け省メモリ設計 (中間 Bitmap 不使用により OOM を抑制)
- イマーシブ全画面表示および端末起動時自動起動 (BOOT_COMPLETED)

### ビルド手順
事前に「必要要件」の Android SDK を用意してください。

```bash
cd android-client

# デバッグ版 (署名済み。端末へそのままインストール可能)
./gradlew assembleDebug
```
生成物: `android-client/app/build/outputs/apk/debug/app-debug.apk`

```bash
# 端末へのインストール
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

> リリース版 (`./gradlew assembleRelease`) は署名設定がないため、`app-release-unsigned.apk` が生成され、署名しないと端末にインストールできません。

## Python 環境 (NDI ブリッジ / シミュレータ)

NDI 入力 (`cyndilib`) とクライアントシミュレータ (`av`) の依存は、リポジトリ直下の `pyproject.toml` で管理され、`.venv` に作成されます。

```bash
make setup-python   # uv sync
```

コントローラはリポジトリ直下から起動してください (`uv run --project .` で `.venv` を参照します)。

## クライアントシミュレータ (tools/simulator/)

実機がなくても、UDP で受信した H.264 ストリームをデコードして Web ブラウザ上で確認できます。Python 環境はリポジトリ直下の `pyproject.toml` から `uv` で `.venv` として自動作成されます (`make setup-python`)。

```bash
# display-1 (UDP :8554) のシミュレータを起動。ブラウザで http://localhost:9001 を開く
make run-simulator
```

## 設定 (config.yaml)

サーバポート、全体仮想キャンバス解像度、各ディスプレイの宛先IP/ポートおよびクロップ座標を設定します。

> 以下の例の `ip: "127.0.0.1"` はローカル検証 (シミュレータ) 用です。実機で運用する場合は、各ディスプレイ (Android クライアント) の実際の IP アドレスに変更してください。

```yaml
server:
  http_port: 8080
  default_stream_port: 8554
  ws_ping_interval_sec: 5

canvas:
  width: 5760
  height: 540
  fps: 30

displays:
  - id: "display-1"
    name: "Display Left"
    ip: "127.0.0.1"
    port: 8554
    width: 1920
    height: 540
    crop_x: 0
    crop_y: 0
    bezel_padding_right: 0

  - id: "display-2"
    name: "Display Center"
    ip: "127.0.0.1"
    port: 8555
    width: 1920
    height: 540
    crop_x: 1920
    crop_y: 0
    bezel_padding_right: 0

  - id: "display-3"
    name: "Display Right"
    ip: "127.0.0.1"
    port: 8556
    width: 1920
    height: 540
    crop_x: 3840
    crop_y: 0
    bezel_padding_right: 0

media:
  default_mode: "testpattern" # "testpattern", "video", または "ndi"
  video_file: ""

pipeline:
  encoder: "auto" # "auto" (VA-API優先・自動フォールバック), "vaapi", または "software"
  vaapi_device: "/dev/dri/renderD128" # VA-API デバイスノード
```

---

## ライセンス

[MIT License](LICENSE)
