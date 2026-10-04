export interface DisplayMetrics {
  id: string
  name: string
  target: string
  is_connected: boolean
  fps: number
  bitrate_kbps: number
  packets_sent: number
  bytes_sent: number
  frames_sent: number
  drops: number
  last_sent_at: string
}

export interface DisplayConfig {
  id: string
  name: string
  ip: string
  port: number
  width: number
  height: number
  crop_x: number
  crop_y: number
  bezel_padding_right: number
}

export interface AppConfig {
  server: {
    http_port: number
    default_stream_port: number
    ws_ping_interval_sec: number
  }
  canvas: {
    width: number
    height: number
    fps: number
  }
  displays: DisplayConfig[]
  media: {
    default_mode: string
    video_file: string
  }
}

export interface PipelineStatus {
  is_running: boolean
  active_mode: string
  video_file: string
}

export interface NDISourceItem {
  name: string
  stream_name: string | null
  host_name: string | null
}
