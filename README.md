# booth-display

LG Display製 LD290EJS-FPN1 (1920x540) を使用した連接ブースディスプレイを制御・同期表示するためのコントローラシステムです。

Web-GUIを内包したスタンドアロン単一バイナリとして動作し、全体仮想キャンバスの映像をディスプレイごとにクロップ分割して低遅延UDP (H.264 Annex-B) で同時送出します。

詳細なアーキテクチャやプロトコル仕様は [docs/](docs/) を参照してください。
- システムアーキテクチャ: [docs/architecture.md](docs/architecture.md)
- 映像伝送・同期プロトコル仕様: [docs/streaming-protocol.md](docs/streaming-protocol.md)
- 開発時の注意事項・環境設定: [docs/development.md](docs/development.md)

## 必要要件

- OS: Linux (Debian 12+, Ubuntu 22.04+, Fedora 39+ など)
- Go: 1.22 以上
- Node.js: 20 以上
- FFmpeg: 6.0 以上 (libx264 有効)
- uv: Python パッケージマネージャ (NDI 入力利用時)

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
bin/display-controller -config config.yaml

# または make コマンドで起動
make run
```

起動後、ブラウザで以下のURLを開きます。
- Web-GUI ダッシュボード: `http://localhost:8080`

---

## 設定 (config.yaml)

サーバポート、全体仮想キャンバス解像度、各ディスプレイの宛先IP/ポートおよびクロップ座標を設定します。

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
```

---

## ライセンス

[MIT License](LICENSE)
