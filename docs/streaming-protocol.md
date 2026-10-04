# 映像伝送・プロトコル設計書 (Streaming Protocol & Low Latency Design)

本書では、エンコード遅延の削減とネットワーク帯域制限（100BASE-TX）を両立するためのストリーミングプロトコル選定およびパケット設計について記述します。

---

## 1. 帯域・遅延トレードオフの比較分析

### (1) 画面1台あたりの通信量計算 (解像度: 1920 × 540)

| 形式 | 1フレームあたりのデータ量 | 30 fps 時の必要帯域 | 60 fps 時の必要帯域 | 100BASE-TX (実効 ~90Mbps) での可否 |
| :--- | :--- | :--- | :--- | :--- |
| 完全無圧縮 (RGB24) | 1920 × 540 × 3 = 3.11 MB | 746.5 Mbps | 1,493 Mbps | 不可 (帯域大幅超過) |
| 完全無圧縮 (YUV420p)| 1920 × 540 × 1.5 = 1.55 MB | 373.2 Mbps | 746.5 Mbps | 不可 (帯域大幅超過) |
| 低負荷圧縮: MJPEG (Quality 80) | 約 80 KB 〜 120 KB | 19.2 〜 28.8 Mbps | 38.4 〜 57.6 Mbps | 可能 |
| 低遅延 H.264 (Baseline, zerolatency) | 平均 約 17 KB (既定 4000 kbps 指定、30 fps 時) | 4.0 Mbps (既定。シーンにより変動) | 8.0 Mbps (同画質を保つ場合の目安) | 極めて余裕 |

### (2) 採用方針
- 完全無圧縮 (RAW):
  - 100BASE-TX LANの物理上限（100Mbps）を大幅に超過するため採用不可。
- 採用方針: 低遅延 H.264 (ゼロレイテンシ設定):
  - 送信側エンコード遅延: x264（preset=ultrafast, tune=zerolatency, Bフレームなし）により5〜10ms程度。
  - 受信側デコード遅延: Android 7.1の MediaCodec (Cortex-A17 VPU) により3〜5ms程度。
  - 必要帯域: 既定で 1台あたり 4 Mbps (x264 の `-b:v` / `-maxrate` = 4000k、シーンにより変動) と低く、ネットワーク輻輳・パケットロスのリスクが最小。
  - Android端末のCPU負荷・発熱が低く、1GB RAM環境でも安定動作。
- MJPEG の位置づけ:
  - MJPEG は Web-GUI 向けプレビュー (`/api/preview/mjpeg`) 専用であり、ディスプレイへの UDP 送出は H.264 のみ。
  - パケットの PayloadType `0x02 (JPEG)` は予約値で、現状の UDP 送出では使用していない。

---

## 2. パケット伝送プロトコル仕様 (UDP Streaming)

通信オーバーヘッドと遅延（TCPの再送待ちによる頭止め遅延: Head-of-line blocking）を排除するため、UDPベースの軽量プロトコルを採用しています。

### (1) パケットフォーマット (Custom Lightweight Header)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Magic (0xBD) | Version (0x01)| PayloadType(1)| Flags (Mark)  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Sequence Number                         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Timestamp (32-bit ms)                    |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          Fragment Index       |        Fragment Total         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Payload (NAL Unit / JPEG data)          |
|                             ....                              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

- Magic: 0xBD (booth-display)
- Version: 0x01
- PayloadType:
  - 0x01 = H.264 NAL Unit (Annex-B)
  - 0x02 = JPEG
  - 0x03 = RAW (定義のみ。現状のパイプラインでは使用していない予約値)
- Flags (ビットフラグ。OR で組み合わせる):
  - 0x01 (Marker): フレーム末尾パケットであることを示す
  - 0x02 (Keyframe): SPS/PPS または IDR スライスを含むことを示す
- Sequence Number: パケット抜け・順序入れ替わりの検知用（32ビット連番）
- Timestamp: コントローラ基準の再生タイムスタンプ（ミリ秒）
- Fragment Index / Total: 1フレーム(NAL)をペイロード上限で分割して再構築するためのインデックス (各16ビット)

#### バイトオーダーとサイズ
- ヘッダは固定 16 バイト。複数バイトのフィールド (Sequence Number / Timestamp / Fragment Index / Fragment Total) はすべて ネットワークバイトオーダー (Big Endian)。
  - オフセット: Magic=0, Version=1, PayloadType=2, Flags=3, Sequence Number=4–7, Timestamp=8–11, Fragment Index=12–13, Fragment Total=14–15
- 最大ペイロードは 1400 バイト (`DefaultMaxPayloadSize`)。
  - 1400 (ペイロード) + 16 (独自ヘッダ) + 28 (IPv4 20 + UDP 8) = 1444 バイトとなり、標準的な Ethernet MTU 1500 バイト以内に収まるため、IP フラグメンテーションが発生しない。
