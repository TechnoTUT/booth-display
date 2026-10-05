# 開発ガイド & 注意事項 (Development Guide & Notes)

本書では、booth-display の開発・保守における注意事項、依存関係、テスト方法について記述します。

---

## 1. 開発環境・必要ツール

- Go: 1.26 以上 (`controller/go.mod` の `go 1.26.7` に準拠)
- Node.js: v20 以上
- uv: Python パッケージマネージャ
- FFmpeg: 6.0 以上 (`libx264` 有効、VA-API 利用時は `h264_vaapi` 有効)
- Intel VA-API ドライバ (`intel-media-driver` / `libva-utils` 等)
- JDK 17 以上 / Android SDK (Android クライアントのビルド時)
- NDI ライブラリ: cyndilib 仮想環境 (リポジトリ直下の `.venv`。`make setup-python` で作成)

### Android クライアントの開発環境
- JDK 17 以上、Android SDK (`platforms;android-34`、`build-tools;34.0.0`) が必要です。
- SDK の場所は `android-client/local.properties` の `sdk.dir=...` (git 管理外) または環境変数 `ANDROID_HOME` で指定します。
- ビルド: `cd android-client && ./gradlew assembleDebug` (出力: `app/build/outputs/apk/debug/app-debug.apk`)

### クライアントシミュレータ
- 実機なしで UDP ストリームを受信・デコードし、Web ブラウザで確認するツールです (`tools/simulator/client_simulator.py`)。
- 依存 (`av` / `opencv-python` / `numpy`) はリポジトリ直下の `pyproject.toml` で管理され、`.venv` に作成されます。
- 起動: `make run-simulator` (`make setup-python` を自動実行) → `http://localhost:9001`

---

## 2. 開発時の注意事項

### (1) Web-GUI アセットの同梱仕様
- Vue 3 のフロントエンド (`controller/frontend/`) で生成された `dist/` アセットは、Go バックエンドの `controller/web/embed.go` を介して `embed.FS` でバイナリ内に静的コンパイルされます。
- フロントエンドに変更を加えた場合は、`make build` または `make build-frontend` を実行して再生成してください。
- 開発サーバー (`make dev-frontend`) 使用時は Vite の HMR (Hot Module Replacement) が利用可能です。

### (2) Tailwind CSS v4 のダークモード運用
- Tailwind CSS v4 ではダークモードが標準でメディアクエリ優先となるため、クラス切り替え (`<html class="dark">`) を有効にする目的で `src/style.css` に以下を定義しています。
  ```css
  @custom-variant dark (&:where(.dark, .dark *));
  ```
- テーマ切り替え用ロジックを変更する際は、このディレクティブを削除しないよう注意してください。

### (3) HTTP ストリーミングのタイムアウト管理
- Go 標準の `http.Server` には `WriteTimeout` が設定されていますが、MJPEG プレビュー (`/api/preview/mjpeg`) などの持続的接続では接続が中断されないよう `http.NewResponseController(w).SetWriteDeadline(time.Time{})` でタイムアウトを解除しています。

### (4) NDI プロセスと終了処理
- `cyndilib` は内部で C 言語の NDI ランタイムスレッドを保持しているため、Python プロセス終了時に標準の終了フックでデッドロックする場合があります。そのため、ブリッジスクリプト (`controller/scripts/ndi_bridge.py`) では明示的な `os._exit(0)` による高速プロセス終了を行っています。

### (5) ハードウェアアクセラレーション (VA-API) とパーミッション
- コントローラは起動時、`/dev/dri/renderD128` などの GPU レンダリングノードの存在を検知し、Intel VA-API によるハードウェアエンコードを自動選択します。
- 実行ユーザに GPU デバイスへのアクセス権限限がない場合（パーミッションエラー等）や、非 Intel GPU 環境では自動的に CPU ソフトウェアエンコード (`libx264` ultrafast) にフォールバックします。
- Linux 環境で一般ユーザとして VA-API を利用する場合、ユーザを `render` または `video` グループに所属させてください。
  ```bash
  sudo usermod -aG render $USER
  ```


---

## 3. テストと検証

- 単体テスト・E2Eテスト:
  ```bash
  make test
  ```
  - `controller/internal/protocol`: パケットヘッダのマーシャリング、MTU分割・結合の検証
  - `controller/internal/pipeline`: H.264 NALUパーサーの検証
  - `controller/internal`: コントローラ全体のUDP送出から受信パケット検証までの統合テスト
